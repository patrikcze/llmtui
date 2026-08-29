# llmtui

A keyboard-first Go terminal UI and bounded agent runtime for **local** LLMs —
Ollama, LM Studio, vLLM, llama.cpp, any OpenAI-compatible server, or a GGUF run
in-process. Local-first: no telemetry, no network call the user did not
configure. Audience: developers running models on their own machine.

This is a mature ~35-package codebase, not a scaffold. Read the code before
changing it.

## Commands

All verified passing on `master` @ `1a89731`, Go 1.27.0, macOS arm64.

```bash
make build                # go build -ldflags … -o llmtui ./cmd/llmtui
make check                # fmt + vet + lint + test-race — run before committing
go test -count=1 ./...    # full suite; ~35s
go vet ./...
gofmt -l .                # must print nothing (CI fails on any output)
golangci-lint run ./...   # config .golangci.yml (schema v2); currently 0 issues
govulncheck ./...         # CI runs this; currently no vulnerabilities
```

Race subset CI actually gates on (faster than `-race ./...`):

```bash
go test -race -count=1 ./internal/agent/... ./internal/agentverify/... \
  ./internal/mcp/... ./internal/provider/... ./internal/tools/... ./internal/tui/...
```

Not runnable in a plain checkout:

- `internal/provider/embedded/llamart` integration tests **skip silently**
  unless `LLMTUI_TEST_GGUF`, `LLMTUI_TEST_CPU` and `YZMA_LIB` are set (see
  `.github/workflows/ci.yml` `native-integration`). A green `go test ./...`
  does *not* mean native inference was exercised.
- `make dist-archive` must run on the native target OS/arch — it installs and
  hash-verifies that platform's llama.cpp binaries (`Makefile:179`).

## Architecture

Full package-by-package map, dependency shape, and the "why do `skill` /
`memory` / `runtime` appear in several folders" cross-check:
**`docs/architecture/package-map.md`**. ADRs: `docs/architecture/decisions/`.
Per-topic docs live in `docs/` (18 files) — check there before inferring
behavior from source.

Five things that are easy to get wrong:

1. **`internal/runtime` is not Go's `runtime`.** It resolves, hash-verifies and
   installs llama.cpp shared libraries. Where both are needed, stdlib is
   aliased `goruntime` (`internal/runtime/platform.go:8`).
2. **Embedded inference binds llama.cpp without cgo** — `purego`/`yzma`, no
   `import "C"` anywhere. But that is *not* the same as `CGO_ENABLED=0`:
   darwin and android release builds set `CGO_ENABLED=1` because Metal spawns
   native threads that need Go's real cgo runtime; Linux and Windows stay at
   `0` (`Makefile:18`, `docs/architecture/embedded-local-inference.md:126`).
   All native contact is confined to `internal/provider/embedded/llamart` so
   every other package builds and tests with no llama.cpp installed.
3. **Dependencies point one way.** `internal/tui` is the hub (~25 internal
   imports); nothing imports it but `internal/cli`. Lower layers never import
   upward, and some package docs state the ban explicitly
   (`internal/memoryindex/types.go:1-8`). Adding an upward import is a design
   error, not a convenience.
4. **Providers do not own timeouts.** No global `http.Client.Timeout` — a
   stream can take minutes. Connect timeout belongs in
   `internal/app/factory.go`; the inactivity watchdog belongs in
   `internal/tui/pipeline.go:1406` (`startRequest`).
5. **The raw user message is never rewritten.** Memory, RAG, skills, summaries
   and model hints are separate labeled sections in `internal/prompt/compose.go`;
   the user's text goes in last, verbatim. See `docs/prompt-composition.md`.

## Conventions

Each rule below is what the code already does — match it, don't improve on it.

- **Errors**: wrap with `fmt.Errorf("context: %w", err)`
  (`internal/cli/root.go:114`). 397 of 740 `fmt.Errorf` calls use `%w`. No
  `pkg/errors`. Do not panic for normal runtime failures.
- **Logging**: there is none. No `slog`, `log`, `logrus`, or `zap` anywhere in
  non-test code. Diagnostics surface through the TUI, `llmtui doctor`, and
  `--debug`. Do not introduce a logger to "help debugging".
- **Config precedence**: changed flags > `LLMTUI_*` env > YAML > defaults.
  Only flags the user actually set are bound (`f.Changed`,
  `internal/cli/root.go:110-116`) so an unset `--temperature` cannot clobber a
  configured value with `0`. Env prefix constant:
  `internal/config/config.go:18`.
- **File permissions**: config and state written `0o600`, dirs `0o755`, writes
  are temp-file + `Chmod` + rename (`internal/config/config.go:1246-1315`).
- **Secrets**: never in logs, cache keys, command env, debug output, or
  `config show`. `--api-key`'s own help text warns it is visible in the process
  list and points at `LLMTUI_API_KEY` / `api_key_env`
  (`internal/cli/root.go:53`).
- **Untrusted text** (provider, MCP, web, RAG output) goes through
  `internal/terminaltext.Sanitize` before rendering and
  `internal/untrusted.Frame` before entering a prompt. Both are deliberately
  below the TUI — don't reimplement either locally.
- **Provider contract**: `internal/provider/provider.go:320`. `Chat` returns a
  channel, emits deltas/reasoning, finishes with exactly one `EventDone` or
  `EventError`, closes the channel, and honors context cancellation. Providers
  holding resources implement `Closer` (`provider.go:332`); callers must
  `CloseProvider` on switch and exit.
- **Cross-platform code**: split by `//go:build` into `*_unix.go` /
  `*_windows.go` / `*_other.go` (`internal/procutil/`, `internal/runtime/`,
  `internal/tools/local_context_disk_*.go`). Tests follow the same suffixes.
- **Tests**: stdlib `testing` only — **no testify**. Table-driven with named
  subtests where it fits (27 files). `httptest` via `internal/testutil`.
  Deterministic — temp dirs and fakes, not sleeps. Test files sit beside the
  code as `x_test.go` in the same package.
- **Package docs**: every package has one, and several encode contracts
  (import bans, deliberate non-goals). Read it before editing the package;
  update it when the contract changes.
- **Commits**: Conventional Commits scoped to the package —
  `feat(tools):`, `fix(tui):`, `test(web):`, `docs:`. One branch + one PR per
  change (`feat/…`, `fix/…`, `docs/…`). **No AI-attribution trailers**
  (`Co-Authored-By`, `Generated with`) — the history has none, keep it that way.

## Workspace Tool Safety Invariants

`internal/tools` and `internal/mcp` let a model read/write files and run shell
commands. Every one of these came from a confirmed bug (see
`docs/architecture/v1-security-review.md`). Any change touching path
resolution, command classification, or the approval flow must preserve all of
them **and add a regression test for the specific case it touches**:

- Confinement is enforced on the resolved, symlink-evaluated path — never
  checked-and-discarded because the target doesn't exist yet. A not-yet-existing
  file inside a symlinked directory must still resolve through the symlink
  before the boundary check.
- `run_command` confines file-system access exactly like
  `read_file`/`write_file`/`list_dir`. `cmd.Dir` alone does not stop an
  allowlisted read command (`cat`, `grep`, `find`, …) from being handed an
  absolute or `../` path.
- Command classification inspects enough of the line that a "read-only" verdict
  cannot come from a destructive subcommand or flag (an allowlisted read-only
  `git` subcommand must not also cover its mutating forms, e.g. branch deletion
  or remote changes).
- `IsSecretPath` and friends classify the *logical* path — shell quoting,
  escaping, and normalization tricks must not defeat detection.
- A pending tool approval is always what the next keypress resolves. If any
  other input-owning UI state can be open at the same moment, the approval takes
  visible precedence — an unrelated "dismiss" must never silently approve.
- The response cache key covers everything that varies the request (history,
  the fully composed system prompt including tool instructions, RAG/memory
  context) — never a subset that can collide across different requests, and
  never an API key.

## Gotchas

- `go test ./...` passing does not cover native inference or the release
  archive; both are CI-only paths (see Commands).
- `make lint` **silently skips** when `golangci-lint` is absent
  (`Makefile:98-102`). "Lint passed" from `make check` may mean "lint didn't
  run" — call `golangci-lint run ./...` directly if you need certainty.
- `.golangci.yml` excludes `errcheck` on deferred `Close`, `Fprint*`,
  `json.Encoder.Encode`, `os.Remove`. Those unchecked returns are intentional.
- `go.mod` has `replace github.com/jupiterrider/ffi => ./third_party/ffi` to
  keep libffi lazy so non-embedded providers start on Linux without
  `libffi.so.8`. Don't remove it while tidying.
- The Charm imports are `charm.land/{bubbletea,bubbles,lipgloss,glamour}/v2`,
  not `github.com/charmbracelet/*`. Charts are hand-written in
  `internal/tui/components/` — there is no charting dependency.
- `Update` must never block; long work returns a `tea.Cmd`.
- Optional subsystems (tools, web, RAG, MCP, memory, agent) are **off by
  default** and a broken/disabled one must not block normal chat startup.
- Declaring an MCP server starts nothing; only an explicit connect launches a
  subprocess.
- `internal/tui/app.go` (~3000 LOC), `commands_local.go` (~1800),
  `internal/tools/tools.go` (~1200) and `internal/config/config.go` (~1300) are
  known size hot-spots. Add to them reluctantly; extraction targets are listed
  at the end of `docs/architecture/package-map.md`.

## Out of scope — ask first

- `third_party/ffi/` — vendored upstream, plus the `go.mod` replace above.
- `internal/runtime/pin.json` and the embedded trusted digests — changing a pin
  changes what binaries users download and verify against.
- `LICENSE`, `THIRD_PARTY_NOTICES.md`, `licenses/` staging in the Makefile.
- `.github/workflows/` and the `dist-*` Makefile targets (release surface).
- Removing or loosening any guardrail, approval prompt, or SSRF check.
- Adding a dependency, or running `go mod tidy` as a drive-by.
- Adding a tool that deletes files.

If the repo is inconsistent on something not listed here, say so and ask —
do not pick a winner and call it the convention.
