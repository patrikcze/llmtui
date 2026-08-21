---
schema_version: 1
id: review-and-fix
name: Review & Fix Loop
version: 1.0.0
description: Agentic defect hunting — review code for real bugs, confirm each finding with evidence, then fix and verify them one at a time. Use when the user wants code reviewed for bugs, a bug diagnosed and fixed, or a "find and fix the problems" pass over a file, package, or diff.
tags:
  - review
  - debugging
  - bugfix
  - agent
  - quality
triggers:
  - review and fix
  - find and fix bugs
  - debug this
  - why is this broken
  - hunt for bugs
recommended_tools:
  - glob
  - grep
  - read_file
  - write_file
  - run_command
capabilities:
  tool_calling: required
---

# Review & Fix Loop

Find real defects, prove each one, fix them one at a time, verify every fix.
Run the four phases in order; never fix during review and never review
during fixing.

## Phase 1 — Scope

1. Pin down what is under review: a diff (`run_command` with
   `git diff` / `git log --oneline`), a file, a package, or a reported
   symptom. If the user gave a symptom, start from the code that produces it.
2. Map the territory before judging it: `glob` and `grep` to find the
   relevant files, `read_file` to read them fully — including callers,
   error paths, and existing tests. Never review code from its name or
   from memory.
3. Learn how the project verifies itself (test command, build command,
   linter). You will need it in phase 4.

## Phase 2 — Review

Hunt for defects that change behavior, in rough priority order:

- Wrong logic: inverted conditions, off-by-one, wrong operator, unreachable
  branches, bad boundary handling (empty, zero, nil/null, max).
- Error handling: ignored errors, swallowed exceptions, error paths that
  leak resources or leave state half-updated.
- Lifecycle and resources: things opened but not closed, missing cleanup on
  early return, use after close/free.
- Concurrency: unsynchronized shared state, channels or locks that can
  deadlock, results that depend on timing.
- Contract breaks: callers that violate what the callee assumes, APIs whose
  documented behavior differs from the code.
- Injection and trust: unvalidated external input reaching commands, paths,
  queries, or rendered output.

Rules of the hunt:

- Trace the actual data flow for each suspicion — read the called code, do
  not assume what it does.
- Style, naming, and taste are out of scope here; note at most a one-line
  aside. (For standards and spec conformance, use a dedicated review skill.)
- Record each candidate as: file:line, the defect in one sentence, and the
  concrete input or sequence that triggers it.

## Phase 3 — Triage

1. Confirm every candidate before it becomes a finding: re-read the exact
   code path and state the failing scenario. If you cannot describe an
   input or sequence that makes it misbehave, it is not a finding — drop it
   or mark it explicitly as "unconfirmed suspicion".
2. Rank confirmed findings by severity: data loss / security first, then
   wrong results, then crashes, then leaks and degradation.
3. Report the ranked list to the user before changing anything. If the user
   asked only for a review, stop here — fixing starts only when asked.

## Phase 4 — Fix, one bug per cycle

For each finding, in severity order, run one full cycle before touching the
next:

1. **Reproduce.** Demonstrate the bug first: a failing test is best
   (`write_file` the test, `run_command` to watch it fail); otherwise a
   command or a precise trace through the code. No reproduction, no fix —
   if it truly cannot be demonstrated, say so and get the user's go-ahead.
2. **Fix the cause.** Make the minimal change at the root cause, not where
   the symptom surfaces. Read the file before writing it; `write_file`
   replaces the whole file, so write the complete content and keep
   untouched parts exactly as read.
3. **Verify.** Re-run the reproduction and watch it pass, then run the
   project's test suite to catch regressions. A fix that breaks another
   test is not done.
4. **Log it.** One line: finding → cause → fix → proof. Then move to the
   next finding.

Never batch unrelated fixes into one edit — a cycle that goes wrong must be
easy to undo and easy to blame.

## Final report

- Findings table: severity, location, defect, status (fixed / reported /
  unconfirmed).
- For each fix: root cause, the change, and the verification that proves it
  (real command output from this session).
- Anything left: suspicions not confirmed, fixes deferred, tests that were
  already failing before you started.
- Never claim a bug is fixed without a passing reproduction, and never
  claim the suite passes without having run it in this session.
