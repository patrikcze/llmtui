# v1 Security Review

This document does not repeat a from-scratch threat model. A full security
review already exists at `security-review/` (baseline commit `ed2ea82`,
2026-07-19: `EXECUTIVE_SUMMARY.md`, `SECURITY_FINDINGS.md`,
`BUGS_AND_RELIABILITY.md`, `REMEDIATION_PLAN.md`, `AI_AGENT_GAP_ANALYSIS.md`,
`findings.json`). This document's job is to state, as of the v1 baseline
(`0458b5f`), what from that review is still open, what is carried forward
as accepted risk, and what is new since that review.

## 1. Prior findings: status at v1 baseline

All 19 tracked findings from the prior review (10 security, 9
bug/reliability) have a traceable remediation commit landed in the dense
`d2641cc`→`e6701be` sequence (2026-07-19/20), immediately following the
review. This was cross-checked against `git log` and spot-verified directly
in this session (not taken only on report): `toolDepth = 0` is present in
`retryLast()` (`internal/tui/app.go:1280`), and `HistoryHash` is present in
`cache.Key` and wired through `pipeline.go:435`. `findings.json` is
internally consistent with the narrative `.md` files — same 19 IDs,
severities, and evidence strings.

| Finding class | Status | Fix commit(s) |
| --- | --- | --- |
| SEC-001..004 (workspace confinement, symlink escape, git subcommand bypass, quote-splitting) | Fixed | `d2641cc` |
| SEC-005 (unbounded provider memory) | Fixed | `66e3538` |
| SEC-006 (terminal escape sanitization) | Fixed | `5c95d52` (new `internal/terminaltext/sanitize.go`) |
| SEC-007 (workspace-skill auto-activation) | Fixed | `cdff3e1` |
| SEC-008 (config show leaks MCP env) | Fixed | `ec19ff9` |
| SEC-009 (env sanitizer gaps) | Fixed (low-confidence attribution — folded into the `d2641cc`/`ab8d016` window, not independently named) | — |
| SEC-010 (unbounded MCP stdio frame) | Fixed | `0d70c1a` |
| BUG-001, BUG-008 (keysMode preempts pending approval) | Fixed | `152c80f` |
| BUG-002, BUG-003 (native execution sync/uncancellable, unserialized) | Fixed | `ef65de0` (`Runner.ExecuteContext` + execution gate) |
| BUG-004 (no durable operation log) | Fixed | `253da4f` (new `internal/history/operations.go`) |
| BUG-005, BUG-007 (duplicate tool-call IDs, discarded unmarshal errors) | Fixed | `ed91570` |
| BUG-009 (Windows KillGroup no-op) | Fixed | `ab8d016`, `038c011` |
| Windows path-guardrail portability | Fixed | `e6701be` |
| `fix-security-and-correctness-bugs.md` Phase 1-10 (symlink-escape-for-new-files, `run_command` confinement, git branch/remote classification, quoting, approval routing, `/retry` budget reset, cache-key history+prompt composition, atomic history save, modelprofile word-boundary matching, contextmgr fence-line capping) | Fixed | `f678bfc` (squashed v0.7.0 pass) |

**No security or bug/reliability finding from the prior review is confirmed
open.** This is an unusually clean result for a project of this scope; the
commit-per-finding traceability is what makes it verifiable rather than
merely asserted.

## 2. Carried-forward items and closure status

These are not part of the severity-scored 19 findings; they are
lower-priority architectural items from `REMEDIATION_PLAN.md`'s "medium
term" table and `fix-security-and-correctness-bugs.md`'s stretch phase:

1. **Capability policy granularity** (`REMEDIATION_PLAN.md` item 16).
   `7d8fa97 fix(approvals): scope temporary capability grants`
   (`internal/tools/approval_policy.go`) added *temporary* scoped grants
   the same day the item was raised. Whether it reaches the full
   per-tool/per-path-glob granularity the remediation plan envisioned was
   **not independently re-verified** in this session — flagged as
   suspected-partial, not confirmed-complete. Not a v1 release blocker;
   recommended as a follow-up issue (see `v1-migration-plan.md`).
2. **Untrusted-content framing — closed in the final v1 slice.**
   (`AI_AGENT_GAP_ANALYSIS.md` Phase 11 item 2). `7934dc8 fix(prompt):
   label untrusted content provenance` (`internal/prompt/compose.go:45-57`)
   previously used prose only. `internal/untrusted.Frame` now adds a
   deterministic, matching, collision-checked begin/end boundary around RAG,
   web, and MCP content while retaining provenance and the existing prose
   guidance. Tests cover an injected fake end marker. This is defense in
   depth for model interpretation, not an authorization boundary; controller
   approvals and tool policy remain authoritative.
3. **RAG content-based secret scanner — closed in the final v1 slice.**
   (`fix-security-and-correctness-bugs.md` Phase 11 item 1, explicitly
   flagged optional/stretch at the time). Filename filtering is now
   supplemented by bounded, high-confidence
   content patterns for private keys and common credential/token/header
   shapes. A match drops the whole file without logging the value. The index
   schema is versioned; pre-scanner indexes and tampered current indexes are
   rejected on load so persistence cannot bypass the scanner. The heuristic
   is deliberately not claimed to detect every possible secret.

## 3. New findings at v1 baseline

### 3.1 Supply chain: `GO-2026-5970` (fixed)

`govulncheck ./...`: infinite loop on invalid input in
`golang.org/x/text@v0.38.0`, fixed in `v0.39.0`. It was reachable via
`internal/skill/manager.go:661` and `internal/history/usage.go:61`. A local
denial-of-service on malformed input reaching Unicode normalization — not
remotely triggerable without an attacker already able to feed skill-catalog
text or history files. The dependency is now `v0.39.0`; the final
`govulncheck ./...` reports no vulnerabilities.

### 3.2 Budget-exhaustion as a resource-exhaustion risk (new framing, not a new code finding)

`v1-audit.md` §4.2's confirmed defect (agent hard budgets not enforced
live) is primarily a correctness/UX issue, but it has a security-relevant
dimension worth stating explicitly per master-prompt §8: an unattended or
repeatedly-approved run that never reaches a cycle boundary is a local
resource-exhaustion vector (API cost, local compute, wall-clock — bounded
only by the 30-minute `max_elapsed` timeout, which **is** enforced live).
This is not a new finding requiring separate remediation — it is the same
defect tracked in `v1-agent-runtime.md` §4, restated here so it is visible
in the security document's scope, per master-prompt §7 requiring the
audit and security documents to cross-reference rather than silo related
risks.

## 4. Areas reviewed with no new findings

Consistent with `v1-audit.md`'s package-level read: MCP stdio framing
(bounded, per SEC-010 fix), workspace path confinement (bounded, per
SEC-001..004 fixes and `CLAUDE.md`'s Workspace Tool Safety Invariants),
tool-call ID correlation, cache-key composition, stale-event rejection, and
truncation handling all show no security regression at this baseline.

## 5. v1 security acceptance checklist (master-prompt §13 gate)

| Requirement | Status |
| --- | --- |
| `govulncheck` run and reviewed | Done — clean after the `golang.org/x/text` bump (§3.1) |
| Direct dependencies reviewed | Done — direct `go.mod` declarations reviewed; no new dependency was introduced by the final slice |
| No secrets or generated local data committed | Re-checked in the final working tree; the user-supplied governing prompt remains intentionally untracked |
| Prior security review findings re-verified, not just cited | Done (§1) |
| New findings from this baseline documented | Done (§3) |
