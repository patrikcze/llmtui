# ADR 0007: Optimistic versioned edits with atomic publication

Status: Accepted
Date: 2026-09-22

## Context

`write_file`/`edit_file` shared one publication primitive that opened the
target path directly with `O_CREATE|O_WRONLY|O_TRUNC`. A caller-supplied
"expected current content" precondition was checked once, before that
truncating open — leaving a final race window between the check and the
write, and no way to bind an edit to a specific prior *read* rather than an
inline string the model had to reproduce exactly. Prose in
`docs/tools-architecture.md`/`docs/security.md` claimed a concurrent
external change was "never (silently) clobbered," which overstated what a
single optimistic check against `O_TRUNC` actually guarantees.

## Decision

The publication primitive was replaced with a confined stage-then-rename
sequence (`internal/tools/file_write.go`): content is written to a random,
initially owner-only sibling file under the same `*os.Root` as the eventual
target, the sibling's permission bits are set to match the target's (an
fchmod-equivalent on the open handle, never a path-based `chmod` that could
race against a changed identity), the live target is rechecked immediately
before publish when a precondition is in play (closing more of the race
window than the original single pre-write check), and publication commits
with `(*os.Root).Rename` — confirmed, from Go 1.27 stdlib source, to provide
atomic replace-existing semantics on Windows too
(`FILE_RENAME_POSIX_SEMANTICS`/`REPLACE_IF_EXISTS`), so no platform-specific
rename workaround was needed. A write-only admission check rejects symlink
targets and symlinked parent directories outright (`symlink_write_unsupported`)
rather than silently replacing a link instead of its target; existing reads
continue to follow confined in-workspace symlinks unaffected. Separately,
`write_file`/`edit_file` gained an optional `expected_resource_id` (a
complete-file snapshot ID from a prior `read_file`) that binds an edit to a
specific observed full-file version, validated with a closed set of
rejection codes (`resource_unavailable`, `wrong_resource_kind`,
`snapshot_incomplete`, `stale_source`, plus a path-identity check) — when no
ID is supplied, the original exact-text-only precondition remains available
and is explicitly labeled as such, never silently treated as version-checked.

The residual is documented, not hidden: llmtui-controlled writes serialize
through `Runner.ExecuteContext`'s existing single-slot execution channel
(confirmed to already cover this whole span end to end, so no second
in-package lock was added); a stale version detected before or during
staging is rejected with the original file untouched; but an external writer
landing in the narrow gap between the final recheck and the `Rename` syscall
itself can still be silently overwritten — a single optimistic check from
one process is not a portable compare-and-swap, and the two doc passages
overclaiming otherwise were corrected to say so.

Alternatives considered and rejected: a mandatory hashline or fuzzy-match
edit protocol (rejected — no measured evidence a small local model needs
anything beyond exact-text-only plus optional version binding); claiming the
new mechanism is a full compare-and-swap (rejected — false, and the plan
explicitly forbids overclaiming a residual race away); falling back to
delete-then-rename where atomic replace is unsupported or denied (rejected
— the plan requires failing closed, keeping the original file and cleaning
up only the temp, never a window with no valid file present).

## Consequences

- A model-observed version `A` followed by an external change to `B` cannot
  pass as a checked edit of `A`; the corrected retry against `B` succeeds
  without discarding the external change.
- Every pre-publish failure mode (create/write/short-write/sync/close/
  recheck/rename) leaves the original file byte-for-byte unchanged, proven
  by fault-injection tests at each seam.
- Two required `fsync` calls (staged-file data, best-effort parent-directory
  entry) measurably increase small-write latency (~80–110× on a 1 KiB file
  in local benchmarking) — an accepted, documented cost of the new
  durability guarantee, not a defect, left open for Phase 8 calibration to
  weigh rather than silently tuned away.
