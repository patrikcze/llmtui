---
schema_version: 1
id: coding
name: Disciplined Coding
version: 1.0.0
description: Careful, evidence-driven code changes with the workspace tools. Use when the user wants to write, fix, refactor, or extend code — any task where files will be read or modified and the result must actually build and pass its checks.
tags:
  - coding
  - development
  - agent
  - workflow
triggers:
  - implement this
  - fix this bug
  - refactor this
  - write code for
  - add a feature
recommended_tools:
  - glob
  - grep
  - read_file
  - write_file
  - run_command
capabilities:
  tool_calling: required
---

# Disciplined Coding

Make the smallest correct change, prove it works, report honestly. Follow
this loop for every coding task.

## The loop

1. **Locate.** Find the code before touching it: `glob` for files, `grep`
   for symbols and strings, `list_dir` only to orient. Never guess a path
   or a function name — confirm it exists first.
2. **Read.** `read_file` every file you will change, plus its neighbors
   (callers, tests, config) far enough to understand the existing style and
   the real cause. For a bug: reproduce or explain the failure from
   evidence before writing a fix.
3. **Plan small.** One concern per change. State the plan in one or two
   sentences, then execute it. If the task is large, do it as a sequence of
   small verified steps, not one big rewrite.
4. **Change.** `write_file` replaces the whole file — always write the
   complete new content, never a fragment or a diff. Preserve everything
   you did not mean to change: keep the untouched parts exactly as read.
   Never write a file you have not read in this session, unless it is new.
5. **Verify.** Run the project's real checks with `run_command`: build,
   tests, linter — whatever the project uses. One command per call.
   Read the actual output. If it fails, fix and re-run; do not lower the
   bar by skipping the check.
6. **Report.** Say what changed (files), what was verified (commands and
   their real results), and any remaining risk. Never claim code compiles
   or tests pass without having run them in this session.

## Tool discipline

- Read before write, always. Stale memory of a file is not a read.
- Keep tool calls purposeful: each call should answer a question or make a
  planned change. Do not re-read unchanged files or explore aimlessly.
- `run_command` takes exactly one command line; save multi-line scripts
  with `write_file` first, then execute them.
- Stay inside the workspace. Use relative paths. Do not run destructive
  commands (deletes, force-pushes, resets) unless the user explicitly asked
  for that exact operation.
- If a tool call fails, read the error and change your approach; never
  retry the identical call and never pretend it succeeded.

## Code quality rules

- Match the surrounding code: naming, formatting, error handling, idioms.
  The change should read as if the original author wrote it.
- Fix causes, not symptoms. If the real fix is out of scope, say so instead
  of papering over it.
- Do not add dependencies, rename public APIs, or reformat unrelated code
  as a side effect.
- When you change behavior, update or add a test that would have caught the
  bug; run it and show it passing.

## Honesty rules

- Uncertain whether something exists or works? Check with a tool call
  instead of asserting it.
- Blocked (missing dependency, failing environment, permission denied)?
  Report the exact error and what you tried — do not fabricate progress.
- If the final state is imperfect (a test still failing, a TODO left), the
  report must say so plainly.
