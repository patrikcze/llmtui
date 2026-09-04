# Self-management (`llmtui self`)

`llmtui self` installs llmtui into a managed location and later updates it
safely from the official GitHub Releases of
[`patrikcze/llmtui`](https://github.com/patrikcze/llmtui), without manually
downloading archives.

```text
llmtui self check              # read-only: is a newer release available?
llmtui self update             # download, verify, stage, replace
llmtui self install            # install the running binary for the current user
llmtui self install --system   # install for all users (needs elevation)
llmtui self path               # which executable is running, and how is it managed?
```

None of these commands need a valid `config.yaml` — like `llmtui version`,
they bypass configuration loading entirely, so a broken config can still be
fixed by updating.

## Update source and verification

- The repository (`patrikcze/llmtui`) is hard-coded. Release content can never
  redirect llmtui at a different repository, host, or filesystem path.
- Release discovery uses the GitHub REST API over HTTPS with a bounded
  timeout, an explicit User-Agent, and a capped response size. No
  authentication is required. If `GITHUB_TOKEN` or `GH_TOKEN` is set it is
  used to raise the rate limit; it is never printed, logged, or persisted.
- Draft releases are always ignored. Prereleases are ignored unless `--pre` is
  passed. The "latest" release is the highest [semantic
  version](https://semver.org), not merely the most recently published tag.
- Downloads come only from `github.com` / `*.githubusercontent.com` over
  HTTPS, are size-capped, and are streamed to a private temporary file.
- Before the archive is unpacked, its SHA-256 is compared against the
  release's `checksums.txt`. A mismatch aborts immediately and leaves the
  current installation untouched. When GitHub also exposes an asset content
  digest it is checked as an additional guard.

## Safe extraction

The archive is extracted into a freshly created private directory, and only
these paths are taken from it:

```text
llmtui / llmtui.exe
lib/llmtui/**            (the bundled llama.cpp runtime)
LICENSE, THIRD_PARTY_NOTICES.md
```

Extraction rejects absolute paths, `..` traversal, entries outside the single
release directory, device/pipe entries, oversized entries, and archive bombs.
The only symlinks accepted are the llama.cpp SONAME aliases that sit directly
in `lib/llmtui/runtime/` and point to a sibling file by bare name; everything
else is refused. After extraction the staged `llmtui` executable is
sanity-checked (size and platform magic number) — it is never executed.

## Transactional replacement

`self update` replaces the runtime tree first, then the binary. Each existing
path is renamed aside to a `*.llmtui-bak-*` sibling before the staged
replacement is moved into place; any failure rolls every replacement back. On
success the backups are removed (a backup that cannot be deleted immediately —
the running `llmtui.exe` on Windows — is renamed to a `*.old` marker that a
later `self` run cleans up).

- **Unix/macOS:** staging happens on the same filesystem as the destination,
  so the moves are atomic renames. The running binary is never truncated or
  written in place. Executable permissions are set to `0755`.
- **Windows:** a running `.exe` cannot be overwritten but can be renamed, so
  the current binary is renamed aside and the new one moved in; the stale copy
  is removed on the next `self` run. No helper process is left running.

If any step — download, checksum, extraction, staging, validation — fails, the
working installation is left exactly as it was.

## Runtime compatibility

A release archive contains the llmtui binary **and** the exact llama.cpp
runtime it was tested against, under `lib/llmtui/runtime/`. `self update`
installs both as one payload, so a new binary is never paired with an
incompatible old runtime. The managed layout matches the runtime resolver's
executable-relative search (`<exe>/../lib/llmtui/runtime` on Unix,
`<exe>/lib/llmtui/runtime` on Windows), so the freshly installed binary finds
and verifies its runtime with no extra step.

`self install` installs the **currently running** binary. If that build has an
executable-relative bundled runtime that verifies against the embedded pin, it
is copied alongside. A bare binary with no bundled runtime still installs
fine (network-provider users need nothing more); run `llmtui runtime install`
afterwards if you want embedded local inference.

## Installation scopes

| Scope | Linux/macOS | Windows |
| --- | --- | --- |
| `self install` (user) | `~/.local/bin/llmtui`, `~/.local/lib/llmtui/runtime/` | `%LOCALAPPDATA%\Programs\llmtui\` |
| `self install --system` | `/usr/local/bin/llmtui`, `/usr/local/lib/llmtui/runtime/` | `%ProgramFiles%\llmtui\` |
| `--dest <dir>` | `<dir>/bin/llmtui`, `<dir>/lib/llmtui/runtime/` | same shape under `<dir>` |

A user install never needs administrative rights. A system install normally
does; llmtui **never** invokes `sudo`, `su`, UAC, or PowerShell to escalate —
it fails with an actionable message:

```text
permission denied writing to /usr/local

Re-run from an elevated shell:

  sudo llmtui self install --system
```

llmtui never edits `.bashrc`, `.zshrc`, or other shell profiles. On Unix, if
the target `bin` directory is not on `PATH`, `self install` prints a warning
and leaves the fix to you. On Windows, `self install` adds the install
directory to the persistent `Path` (machine `Path` for `--system`, user
`Path` otherwise), preserving every existing entry and adding no duplicate;
already-open terminals must be reopened to see the change.

## Non-interactive use

`self update` / `self install` prompt for confirmation on an interactive
terminal. Pass `--yes` to skip the prompt. If stdin is not interactive and
`--yes` was not given, the command fails with guidance rather than blocking.
`--dry-run` prints the plan and changes nothing.

## `self path`

```text
Executable: /usr/local/bin/llmtui
Resolved:   /usr/local/bin/llmtui
Version:    v1.0.24
Scope:      system
```

Scopes: `user`, `system`, `unpacked release bundle`, `development/source
build`, or `custom/unmanaged` when llmtui cannot place the executable
confidently. A small non-sensitive `lib/llmtui/install.json` manifest
(version, scope, prefix, asset) is written on install and makes the scope
authoritative. `self path` needs no network access.

## Release-naming contract

`self update` depends on every desktop release publishing, with predictable
names:

```text
llmtui-vX.Y.Z-darwin-amd64.tar.gz
llmtui-vX.Y.Z-darwin-arm64.tar.gz
llmtui-vX.Y.Z-linux-amd64.tar.gz
llmtui-vX.Y.Z-linux-arm64.tar.gz
llmtui-vX.Y.Z-windows-amd64.zip
checksums.txt
```

`.github/workflows/release.yml` asserts these are present before publishing.
