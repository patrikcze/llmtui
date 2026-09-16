# Code quality and anti-slop policy

Practical rules for keeping a mature, ~106k-LOC codebase from drifting into
AI-generated-shaped spaghetti as more of it is written or edited by coding
agents. These are project-specific defaults, not general style opinions —
match them to the code you're touching, don't cite them to block someone
else's judgment call. Background: `.claude/tasks/plans/llmtui-quality-and-anti-slop.md`
(gitignored; the 2026-09 audit that produced this document).

## No speculative abstraction

Don't add an interface with exactly one implementation unless it already
establishes an architectural boundary llmtui relies on (`provider.Provider`,
`tools.WebClient`, `personalapps.MailBackend` — each has a real seam: swap
providers, or a test double) or is needed to keep a package below the TUI
independently testable. A `FooInterface` with one `fooImpl` and no second
implementation on the horizon is not a boundary, it's ceremony.

## No duplicate state

One concept has one owner. Before adding a new boolean/counter/cache to
`tui.Model` (or any state struct), check whether an existing field already
answers the question. If two layers need related-but-distinct facts (e.g.
the TUI's `toolOK`/`toolErr` exit-summary counters vs. the agent's
evidence-ledger `ActionStatus` classification), document *why* their scope
differs in a comment next to the field — don't let the difference be
implicit and rediscovered by the next reader via `grep`.

## No parallel execution paths

Ordinary chat, tool-enabled chat, and `/agent on` share one orchestration
kernel (`dispatch` → `startRequest` → `handleStreamEvent` →
`startToolBatch` → `sendToolResults`; [ADR 0001](architecture/decisions/0001-single-orchestration-kernel.md)).
A new feature that needs "its own" way to reach the model or execute a tool
is a design error, not a shortcut — route it through the existing kernel.

## No helper soup

A helper earns its place by doing one of:

- encoding a meaningful invariant (`internal/tools` confinement checks);
- removing real, verified duplication (`internal/redact.Secrets`, extracted
  after three packages carried byte-identical regexes);
- isolating a side effect (`internal/untrusted.Frame`);
- making a test materially easier to write.

A one-line wrapper that only renames a call (`func Foo(x) { return bar(x) }`
with no added argument, error handling, or doc value) should not exist —
call the wrapped function directly.

## No over-commenting

Comments explain *why*, not *what* — a hidden constraint, an invariant, a
workaround for a specific bug, or behavior that would otherwise surprise a
reader (see `internal/runtime/platform.go`'s `goruntime` alias comment, or
`CLAUDE.md`'s "Workspace Tool Safety Invariants" list, for the level of
specificity to aim for). If deleting the comment wouldn't confuse the next
reader, delete it. Never restate the code in prose, and never leave a
`// TODO: once we have structured logging...` comment describing a system
this codebase has deliberately decided not to have (see CLAUDE.md's
Logging convention) — either wire the thing into an existing surface
(`doctor`, `--debug`) or remove the dead branch and the comment together.

## No unused flexibility

No config field, interface method, enum member, or "future extension
point" without a current caller. A config field that's parsed, validated,
and documented but never read anywhere is a trap: it looks load-bearing
and isn't. If you find one while touching nearby code, either wire it up
or remove it in a separate, explicit change — don't leave it for the next
reader to rediscover.

## No silently weakened tests

Every measurement test is either:

- a **quality gate**, with an explicit pass/fail floor a broken
  implementation can actually fail (see `internal/rag/retrieval_eval_test.go`'s
  `qualityViolations` and its negative characterization test,
  `TestRetrievalQualityGateFailsOnBrokenRetrieval`); or
- explicitly named/documented **report-only** (a `Benchmark*` function, or a
  test that only `t.Logf`s a measurement).

A monotonicity-only check (`recall@k >= recall@1`) is not a quality gate —
an all-zero result satisfies it trivially. If you can't state the minimum
acceptable value for a metric, don't assert on it; log it and say so.
Never mix a timing/benchmark assertion into a correctness gate that must
run identically on any machine.

A model-based/semantic verifier's verdict, or a controller's terminal
status (`DecisionDone`), is evidence — never treat it as the whole proof
that a task actually succeeded. Where a test can check an independent,
deterministic postcondition (file exists with expected bytes, expected
substring in the final answer, no mutation before approval), check that
instead of — or in addition to — the verifier's opinion. See
`internal/tui/live_agent_eval_test.go`'s per-fixture `postcondition`
functions.

## Refactor separately from behavior change

For a risky hotspot (anything touching `internal/tools` confinement,
`internal/tui`'s approval/cancellation state, or provider streaming):

1. Identify or write a characterization test that pins current behavior.
2. Do the extraction/refactor with no behavior change; verify the test
   still passes unchanged.
3. Only then, in a separate change, alter behavior.

Don't bundle "I refactored this while I was in here" with a bug fix or
feature — it makes both harder to review and impossible to bisect.

## Evidence before extraction

Before splitting a large file or function, or introducing a new
sub-state struct on `tui.Model` (following `turnRuntime`, `selectionState`,
`pickerState`, `exitSummaryState`, `composerHistoryState`), answer all six:

1. What responsibility is currently mixed?
2. What invariant becomes easier to express after the split?
3. What state owner becomes clearer?
4. What duplicated logic disappears?
5. Which existing test characterizes/protects the change?
6. Does it reduce coupling, or just move lines?

If you can't answer all six, it's a hypothesis, not a refactor — don't do
it yet. A large file is not automatically defective; `internal/tui/app.go`'s
`Update` is long because a Bubble Tea dispatcher's `switch msg := msg.(type)`
is inherently a flat list of cases, not because logic is tangled — see the
2026-09 audit's complexity report for the reasoning.

## Where a new behavior belongs

Before adding code directly to `app.go`, `agent_loop.go`, `pipeline.go`, or
`commands_local.go` (the known size hot-spots — see
`docs/architecture/package-map.md`'s "Size hot-spots" section), ask: does
this belong to an existing lower-level package or cohesive state owner
instead? Landing it there by default because "that's where the `Model` is"
is exactly how a hot-spot grows past the point anyone can safely change it.
