---
schema_version: 1
id: jira-worklog
name: Jira Worklog Preparation
version: 1.0.0
description: Turn a day's work notes into clear, submission-ready Jira worklog entries.
tags:
  - jira
  - reporting
triggers:
  - prepare my worklog
  - summarize today for jira
capabilities:
  tool_calling: optional
---

# Jira Worklog Preparation

Use this skill to convert informal work notes into Jira worklog entries.

## Workflow

1. Group the notes by issue key. Ask for the key if none is given.
2. For each issue, write one entry: what was done, outcome, next step.
3. Keep entries factual and past tense; no filler.
4. Estimate time per entry only from evidence in the notes; mark guesses
   explicitly as estimates and total them.
5. Present the result as a list the user can paste or submit — never submit
   anything yourself unless an approved tool call is explicitly requested
   by the user.
