---
schema_version: 1
id: jira-task-review
name: Jira Task Review
version: 1.0.0
description: Review a Jira task description for clarity, scope, and testable acceptance criteria.
tags:
  - jira
  - planning
triggers:
  - review this ticket
  - is this task well defined
---

# Jira Task Review

Use this skill to review a Jira task before work starts.

## Checklist

1. Summary states the outcome, not the activity.
2. Scope is bounded: what is explicitly out of scope?
3. Acceptance criteria are testable and enumerated.
4. Dependencies and blockers are named with issue keys.
5. The estimate matches the described scope; flag mismatches.

Report findings as a short list: what is missing, what is ambiguous, and a
suggested rewrite for each weak section.
