package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/tools"
)

// personalAppsScopeFromConfig maps configuration onto the domain package's
// Scope. It never widens what the user configured: an empty allowlist stays
// empty, which Scope itself treats as "authorizes nothing".
func personalAppsScopeFromConfig(c config.PersonalAppsConfig) personalapps.Scope {
	return personalapps.Scope{
		MailEnabled:      c.Mail.Enabled,
		CalendarEnabled:  c.Calendar.Enabled,
		AllowedAccounts:  append([]string(nil), c.Mail.AllowedAccounts...),
		AllowedCalendars: append([]string(nil), c.Calendar.AllowedCalendars...),
		MutationsEnabled: c.Mutations.Enabled,
	}
}

// personalAppsJournalFromConfig creates the user-level mutation journal
// without touching the filesystem. Unlike normal history, the journal must
// survive a workspace change: it is the recovery barrier for an external
// effect whose outcome was interrupted or uncertain.
func personalAppsJournalFromConfig(c config.PersonalAppsMutationConfig) (personalapps.Journal, error) {
	if !c.Enabled {
		return nil, nil
	}
	dir := strings.TrimSpace(c.LedgerPath)
	if dir == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve personal-apps ledger directory: %w", err)
		}
		dir = filepath.Join(configDir, "llmtui", "personal-apps")
	} else {
		var err error
		dir, err = history.ExpandHome(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve personal-apps ledger directory: %w", err)
		}
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("personal-apps ledger path must be absolute")
	}
	return personalapps.NewMutationLedger(dir), nil
}

// blockDependentPersonalAppsApply prevents a model from making preparation
// and application interdependent in one tool response. The approval UI must
// show a plan that already exists, not a speculative plan another call in the
// same batch might create. Reads and prepares in that batch remain runnable;
// only apply calls are rejected with an actionable result.
func blockDependentPersonalAppsApply(plan *toolBatchPlan) bool {
	hasPrepare := false
	for _, call := range plan.calls {
		if call.Tool == tools.ToolPersonalApps && personalapps.PeekOperation([]byte(call.Body)) == personalapps.OpChangePrepare {
			hasPrepare = true
			break
		}
	}
	if !hasPrepare {
		return false
	}
	blocked := false
	for i, call := range plan.calls {
		if call.Tool == tools.ToolPersonalApps && personalapps.PeekOperation([]byte(call.Body)) == personalapps.OpChangeApply {
			plan.block(i, "change_apply cannot share a tool batch with change_prepare; wait for the returned plan and a separate human approval")
			blocked = true
		}
	}
	return blocked
}

// personalAppsLimitsFromConfig maps configuration onto personalapps.Limits.
// A zero/unparseable field is left zero so personalapps.Limits.withDefaults
// applies that package's own documented default — this function must never
// invent a second, different default.
func personalAppsLimitsFromConfig(c config.PersonalAppsLimitsConfig) personalapps.Limits {
	l := personalapps.Limits{
		PageSize:           c.PageSize,
		MaxPageSize:        c.MaxPageSize,
		MaxMessagesPerRead: c.MaxMessagesPerRead,
		MaxBodyBytes:       c.MaxBodyBytes,
		MaxResultBytes:     c.MaxResultBytes,
		MaxScanCandidates:  c.MaxScanCandidates,
		MaxCalendarDays:    c.MaxCalendarDays,
		MaxChangesPerPlan:  c.MaxChangesPerPlan,
	}
	if d, err := time.ParseDuration(c.ReadTimeout); err == nil && d > 0 {
		l.ReadTimeout = d
	}
	if d, err := time.ParseDuration(c.MutationTimeout); err == nil && d > 0 {
		l.MutationTimeout = d
	}
	return l
}

// personalAppsDoctorOverlay is intentionally passive: it checks only
// configuration and the configured helper's filesystem metadata. It never
// launches an app/helper or requests Calendar permission, so opening doctor
// cannot cause a surprise TCC prompt.
func (m *Model) personalAppsDoctorOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("doctor — personal apps") + "\n\n")
	cfg := m.cfg.PersonalApps
	lines := make([]string, 0, 6)
	if !cfg.Enabled {
		lines = append(lines, "✗ personal apps disabled (personal_apps.enabled)")
	} else {
		lines = append(lines, "✓ personal apps enabled")
		if cfg.Mail.Enabled {
			lines = append(lines, fmt.Sprintf("✓ Mail enabled with %d allowed account(s)", len(cfg.Mail.AllowedAccounts)))
		} else {
			lines = append(lines, "✗ Mail disabled (personal_apps.mail.enabled)")
		}
		if cfg.Calendar.Enabled {
			status := personalapps.CheckCalendarHelper(cfg.Calendar.HelperPath)
			prefix := "✗ "
			if status.Ready {
				prefix = "✓ "
			}
			lines = append(lines, prefix+"Calendar: "+status.Message)
			lines = append(lines, fmt.Sprintf("Calendar scope: %d allowed calendar(s)", len(cfg.Calendar.AllowedCalendars)))
			lines = append(lines, "Calendar permission is checked only after you explicitly connect and run an operation")
		} else {
			lines = append(lines, "✗ Calendar disabled (personal_apps.calendar.enabled)")
		}
	}
	for _, line := range lines {
		style := m.theme.StatusValue
		if strings.HasPrefix(line, "✗") {
			style = m.theme.ErrorText
		}
		b.WriteString("  " + style.Render(line) + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("This check is passive: it does not launch Mail, Calendar, or the companion."))
	return m.overlayFooter(&b)
}

// enterPersonalAppsPrivateSession is the Service's PrivacyGate. It runs on
// whatever goroutine executes the tool batch (see tools.Runner.
// ExecuteContext), never assume it is the Bubble Tea Update goroutine — the
// only state it touches is personalAppsPrivate, which is an atomic.Bool for
// exactly this reason.
//
// It always succeeds: there is currently nothing to refuse on (no headless
// GUI-session detection, no remote-model consent prompt). Those are
// documented gaps, not silent gaps — see personalAppsPrivate's doc comment
// and the package README for what a private session does and does not cover
// yet.
func (m *Model) enterPersonalAppsPrivateSession(context.Context) error {
	m.personalAppsPrivate.Store(true)
	return nil
}

// cmdPersonalApps implements /personal-apps status|connect|disconnect. Scope
// (which accounts/calendars are allowed) is deliberately configuration-only
// in this slice — there is no interactive scope editor yet — so connect
// only records the human's decision to use whatever scope is configured;
// it grants nothing beyond that and never itself expands the allowlist.
func cmdPersonalApps(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	if m.personalApps == nil {
		return m.fail("personal apps integration is disabled — enable personal_apps.enabled (and mail/calendar.enabled) in config first")
	}
	switch sub {
	case "", "status":
		m.notice = personalAppsStatusNotice(m.personalApps.Status())
	case "connect":
		return personalAppsConnect(m, rest)
	case "disconnect":
		return personalAppsDisconnect(m, rest)
	default:
		return m.fail("usage: /personal-apps [status|connect mail|calendar|disconnect mail|calendar]")
	}
	return nil
}

func personalAppsConnect(m *Model, target string) tea.Cmd {
	adapter, err := parsePersonalAppsAdapter(target)
	if err != nil {
		return m.fail(err.Error())
	}
	if adapter == personalapps.AdapterCalendar {
		// Connecting is the consent boundary for Calendar. Refuse before
		// recording consent when the configured companion cannot be used, so
		// the next tool call does not fail with the less useful generic
		// "adapter is not connected" message.
		status := personalapps.CheckCalendarHelper(m.cfg.PersonalApps.Calendar.HelperPath)
		if !status.Ready {
			return m.fail("connect calendar: " + status.Message)
		}
	}
	if err := m.personalApps.Connect(adapter); err != nil {
		return m.fail(fmt.Sprintf("connect %s: %s", adapter, personalAppsErrorText(err)))
	}
	m.notice = fmt.Sprintf("📇 personal_apps: %s connected — %s", adapter, personalAppsScopeSummary(m.personalApps.Scope(), adapter))
	return nil
}

func personalAppsDisconnect(m *Model, target string) tea.Cmd {
	adapter, err := parsePersonalAppsAdapter(target)
	if err != nil {
		return m.fail(err.Error())
	}
	m.personalApps.Disconnect(adapter)
	if m.personalAppsApprovals != nil {
		m.personalAppsApprovals.Reset()
	}
	m.notice = fmt.Sprintf("personal_apps: %s disconnected", adapter)
	return nil
}

func parsePersonalAppsAdapter(s string) (personalapps.Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mail":
		return personalapps.AdapterMail, nil
	case "calendar":
		return personalapps.AdapterCalendar, nil
	default:
		return "", fmt.Errorf("usage: /personal-apps connect|disconnect mail|calendar")
	}
}

// personalAppsScopeSummary reports the configured allowlist size for one
// adapter, without naming any account or calendar — a connect confirmation
// is not the place to first disclose what's in scope.
func personalAppsScopeSummary(scope personalapps.Scope, adapter personalapps.Adapter) string {
	switch adapter {
	case personalapps.AdapterMail:
		return fmt.Sprintf("%d account(s) in scope", len(scope.AllowedAccounts))
	case personalapps.AdapterCalendar:
		return fmt.Sprintf("%d calendar(s) in scope", len(scope.AllowedCalendars))
	default:
		return ""
	}
}

// personalAppsStatusNotice renders the human-facing summary for
// /personal-apps status from the same metadata-only StatusView the model
// itself would see via {"operation":"status"}.
func personalAppsStatusNotice(v personalapps.StatusView) string {
	var b strings.Builder
	b.WriteString("📇 personal apps — ")
	parts := []string{
		fmt.Sprintf("mail %s", adapterState(v.MailEnabled, v.MailConnected)),
		fmt.Sprintf("calendar %s", adapterState(v.CalendarEnabled, v.CalendarConnected)),
		fmt.Sprintf("mutations %s", enabledWord(v.MutationsEnabled)),
	}
	b.WriteString(strings.Join(parts, " · "))
	for _, note := range v.Notes {
		b.WriteString("\n  " + note)
	}
	return b.String()
}

func adapterState(enabled, connected bool) string {
	switch {
	case !enabled:
		return "off"
	case connected:
		return "connected"
	default:
		return "enabled, not connected"
	}
}

func enabledWord(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// personalAppsErrorText renders a personal_apps error for a human command
// reply — never for a model-facing result, which already gets its own
// stable Code/Message straight from the Result envelope.
func personalAppsErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// personalAppsApprovalLines renders the pending-approval detail for one
// personal_apps call. For change_apply it looks up the actual prepared
// plan — never trusting the model's own PlanView it echoed back — so the
// human reviews the same summary the plan store itself holds. Anything else
// (a read reaching this prompt through an explicit grant edge case) falls
// back to Describe().
func (m *Model) personalAppsApprovalLines(c tools.Call) (header string, lines []string) {
	raw := []byte(c.Body)
	if personalapps.PeekOperation(raw) != personalapps.OpChangeApply || m.personalApps == nil {
		return c.Describe(), nil
	}
	planID := personalapps.PeekPlanID(raw)
	if planID == "" {
		return "apply an unspecified plan (malformed request)", nil
	}
	plan, err := m.personalApps.PreparedPlan(planID)
	if err != nil {
		return fmt.Sprintf("apply %s — plan not found or expired; denying is safe", shortPersonalAppsPlanID(planID)), nil
	}
	header = fmt.Sprintf("apply personal_apps plan %s — %d item(s), expires %s",
		shortPersonalAppsPlanID(plan.PlanID), plan.ItemCount, plan.ExpiresAt.Local().Format("15:04:05"))
	return header, plan.Summary
}

// shortPersonalAppsPlanID trims the random suffix for display; the full ID
// is still what gets sent back in change_apply.
func shortPersonalAppsPlanID(id string) string {
	const keep = 12
	if len(id) <= keep {
		return id
	}
	return id[:keep] + "…"
}

// approvePersonalAppsCalls records a human "yes"/"always" approval for every
// pending change_apply call, binding it to the exact plan digest currently
// on record — never to whatever the model claims. It must run before the
// batch executes; Service.Execute independently re-verifies this exact
// (plan ID, digest) pair before applying anything; it never trusts the
// caller's word that a plan was approved.
func (m *Model) approvePersonalAppsCalls(calls []tools.Call) {
	if m.personalApps == nil || m.personalAppsApprovals == nil {
		return
	}
	for _, c := range calls {
		if c.Tool != tools.ToolPersonalApps {
			continue
		}
		raw := []byte(c.Body)
		if personalapps.PeekOperation(raw) != personalapps.OpChangeApply {
			continue
		}
		planID := personalapps.PeekPlanID(raw)
		if planID == "" {
			continue
		}
		plan, err := m.personalApps.PreparedPlan(planID)
		if err != nil {
			continue // stale/unknown plan: nothing to approve, Execute will refuse it
		}
		m.personalAppsApprovals.Approve(plan.PlanID, plan.Digest)
	}
}

// denyPersonalAppsCalls clears any pending approval for the denied batch's
// change_apply calls. Not required for safety (Service only ever consults an
// approval a human actually granted), but it keeps the ledger from holding
// an approval for a plan that just got explicitly rejected.
func (m *Model) denyPersonalAppsCalls(calls []tools.Call) {
	if m.personalAppsApprovals == nil {
		return
	}
	for _, c := range calls {
		if c.Tool != tools.ToolPersonalApps {
			continue
		}
		if id := personalapps.PeekPlanID([]byte(c.Body)); id != "" {
			m.personalAppsApprovals.Deny(id)
		}
	}
}
