package tui

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/untrusted"
)

// personalAppsTestModel is newTestModel with the integration enabled and
// mail scoped, so tests exercise the real Service rather than a nil one.
func personalAppsTestModel(t *testing.T, configure func(*config.PersonalAppsConfig)) *Model {
	t.Helper()
	m := newTestModel(t)
	pcfg := config.PersonalAppsConfig{
		Enabled: true,
		Mail:    config.PersonalAppsMailConfig{Enabled: true, AllowedAccounts: []string{"acct-1"}},
	}
	if configure != nil {
		configure(&pcfg)
	}
	m.cfg.PersonalApps = pcfg
	m.cfg.Tools.Enabled = true
	m.toolsOn = true
	m.rebuildFromConfig()
	return m
}

func TestPersonalAppsDisabledLeavesToolAbsent(t *testing.T) {
	m := newTestModel(t) // PersonalApps zero-value: disabled
	if m.personalApps != nil {
		t.Fatal("personalApps was constructed although personal_apps.enabled defaults false")
	}
	if m.toolRunner.PersonalApps != nil {
		t.Fatal("Runner.PersonalApps was wired although the feature is disabled")
	}
	for _, spec := range m.eligibleToolSpecs() {
		if personalapps.Operation(spec.Name).Valid() {
			t.Fatalf("personal_apps operation %q appears in eligibleToolSpecs while disabled", spec.Name)
		}
	}
	m.errText = ""
	cmdPersonalApps(m, "status")
	if m.errText == "" {
		t.Fatal("cmdPersonalApps did not report the feature is disabled")
	}
}

// TestPersonalAppsEnabledOffersOnlyPermittedOperations pins the contract in
// Scope.AllowedOperations: a disabled or disconnected adapter's operations
// are absent from the model-visible catalog rather than present and failing.
// Before this, every operation was offered regardless of connection state,
// so a model called calendar_list on a never-connected Calendar, got
// app_unavailable, and could not tell an ungranted adapter from an outage.
func TestPersonalAppsEnabledOffersOnlyPermittedOperations(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Calendar.Enabled = true
		c.Calendar.AllowedCalendars = []string{"cal-1"}
	})
	if m.personalApps == nil {
		t.Fatal("personalApps was not constructed although personal_apps.enabled is true")
	}
	if m.toolRunner.PersonalApps == nil {
		t.Fatal("Runner.PersonalApps was not wired")
	}

	offered := offeredPersonalAppsOperations(m)
	if !slices.Equal(offered, permittedPersonalAppsOperations(m)) {
		t.Fatalf("offered %v, want exactly the permitted operations %v",
			offered, permittedPersonalAppsOperations(m))
	}
	if slices.Contains(offered, string(personalapps.OpCalendarList)) {
		t.Fatalf("offered %v includes calendar_list while Calendar is not connected", offered)
	}

	if err := m.personalApps.Connect(personalapps.AdapterCalendar); err != nil {
		t.Fatalf("Connect(calendar): %v", err)
	}
	connected := offeredPersonalAppsOperations(m)
	if !slices.Equal(connected, permittedPersonalAppsOperations(m)) {
		t.Fatalf("after connecting, offered %v, want exactly the permitted operations %v",
			connected, permittedPersonalAppsOperations(m))
	}
	// Calendar operations become reachable only where the platform supports
	// them at all, so follow Status rather than asserting a fixed set.
	if want := slices.Contains(permittedPersonalAppsOperations(m), string(personalapps.OpCalendarList)); want &&
		!slices.Contains(connected, string(personalapps.OpCalendarList)) {
		t.Fatalf("after connecting, offered %v omits a permitted calendar_list", connected)
	}
}

func offeredPersonalAppsOperations(m *Model) []string {
	out := make([]string, 0, len(personalapps.Operations()))
	for _, spec := range m.eligibleToolSpecs() {
		if personalapps.Operation(spec.Name).Valid() {
			out = append(out, spec.Name)
		}
	}
	slices.Sort(out)
	return out
}

func permittedPersonalAppsOperations(m *Model) []string {
	ops := m.personalApps.Status().Operations
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, string(op))
	}
	slices.Sort(out)
	return out
}

func TestDoctorPersonalAppsExplainsMissingCalendarHelperWithoutLaunchingIt(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Calendar.Enabled = true
		c.Calendar.AllowedCalendars = []string{"cal-1"}
		c.Calendar.HelperPath = ""
	})
	if cmd := cmdDoctor(m, "personal-apps"); cmd != nil {
		t.Fatal("passive personal-apps doctor unexpectedly returned an async command")
	}
	if !m.overlayOpen {
		t.Fatal("/doctor personal-apps did not open an overlay")
	}
	overlay := m.personalAppsDoctorOverlay()
	wantCalendarDiagnostic := "Calendar requires macOS and the EventKit companion"
	if runtime.GOOS == "darwin" {
		wantCalendarDiagnostic = "calendar.helper_path is not configured"
	}
	for _, want := range []string{
		"doctor — personal apps",
		wantCalendarDiagnostic,
		"This check is passive",
	} {
		if !strings.Contains(overlay, want) {
			t.Errorf("doctor overlay missing %q:\n%s", want, overlay)
		}
	}
}

func TestCmdPersonalAppsConnectCalendarRequiresReadyHelper(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Calendar.Enabled = true
		c.Calendar.AllowedCalendars = []string{"cal-1"}
		c.Calendar.HelperPath = ""
	})

	if cmd := cmdPersonalApps(m, "connect calendar"); cmd != nil {
		t.Fatalf("connect calendar returned an async command: %v", cmd())
	}
	if m.personalApps.Connection().CalendarConnected {
		t.Fatal("Calendar connected although its helper was not ready")
	}
	if m.errText == "" {
		t.Fatal("connect calendar did not explain why Calendar is unavailable")
	}
}

func TestPersonalAppsEmptyAllowlistGrantsNoAccountByDefault(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Mail.AllowedAccounts = nil
	})
	scope := m.personalApps.Scope()
	if len(scope.AllowedAccounts) != 0 {
		t.Fatalf("AllowedAccounts = %v, want empty when config configured none", scope.AllowedAccounts)
	}
	if scope.AllowsAccount("acct-1") {
		t.Fatal("an unconfigured account was authorized")
	}
}

func TestCmdPersonalAppsConnectDisconnect(t *testing.T) {
	m := personalAppsTestModel(t, nil)

	if cmd := cmdPersonalApps(m, "connect mail"); cmd != nil {
		t.Fatalf("connect mail returned a failing command: %v", cmd())
	}
	if !m.personalApps.Connection().MailConnected {
		t.Fatal("mail did not report connected after /personal-apps connect mail")
	}
	if !strings.Contains(m.notice, "connected") {
		t.Fatalf("notice = %q, want it to confirm the connection", m.notice)
	}

	// Calendar was never enabled in config; connecting it must fail closed,
	// not silently succeed.
	m.errText = ""
	cmdPersonalApps(m, "connect calendar")
	if m.errText == "" {
		t.Fatal("connect calendar succeeded although calendar is disabled in config")
	}

	if cmd := cmdPersonalApps(m, "disconnect mail"); cmd != nil {
		t.Fatalf("disconnect mail returned a failing command: %v", cmd())
	}
	if m.personalApps.Connection().MailConnected {
		t.Fatal("mail still reports connected after /personal-apps disconnect mail")
	}

	m.errText = ""
	cmdPersonalApps(m, "connect bogus")
	if m.errText == "" {
		t.Fatal("an unknown adapter name was accepted")
	}
}

func TestCmdPersonalAppsStatusNamesNoAccountOrCalendar(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Mail.AllowedAccounts = []string{"very-secret-account-id"}
	})
	cmdPersonalApps(m, "status")
	if strings.Contains(m.notice, "very-secret-account-id") {
		t.Fatalf("status notice leaked an account identifier: %q", m.notice)
	}
}

// change_apply must always require approval, regardless of /tools auto or
// any standing capability grant — this is the specific safety property the
// architecture plan calls out for this tool.
func TestCallNeedsApprovalForcesApprovalForChangeApply(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Mutations.Enabled = true
	})
	m.toolsAutoApprove = true // the generic bypass this must not honor

	apply := tools.Call{Tool: tools.ToolPersonalApps, Body: `{"operation":"change_apply","arguments":{"plan_id":"plan_1"}}`}
	if !m.callNeedsApproval(apply) {
		t.Fatal("change_apply did not require approval under /tools auto")
	}

	// A standing "always allow this exact body" grant must not help either:
	// approvePersonalAppsCalls/Service binds approval to the plan digest,
	// not to the approvalPolicy grant list, and callNeedsApproval's
	// personal_apps branch runs before Allows() is even consulted.
	m.approvalPolicy.GrantCall(apply, time.Now(), scopedApprovalTTL)
	if !m.callNeedsApproval(apply) {
		t.Fatal("change_apply did not require approval despite a standing grant for the identical body")
	}

	read := tools.Call{Tool: tools.ToolPersonalApps, Body: `{"operation":"status"}`}
	if m.callNeedsApproval(read) {
		t.Fatal("a read/status call required approval although mail is connected and reads are gated by connect, not per-call approval")
	}
}

func TestApprovePersonalAppsCallsBindsToPlanDigest(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Mutations.Enabled = true
	})
	if err := m.personalApps.Connect(personalapps.AdapterMail); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Prepare a real plan through the Service exactly as the model would.
	res := m.personalApps.ExecuteRaw(context.Background(), []byte(
		`{"operation":"change_prepare","arguments":{"changes":[`+
			`{"type":"mail_set_flag","messages":[{"message_id":"`+testMessageHandle(t, m)+`","expected_version":"v1"}],"flagged":true}`+
			`]}}`))
	if res.Error != nil {
		t.Fatalf("change_prepare failed: %s: %s", res.Error.Code, res.Error.Message)
	}
	plan := res.Data.(personalapps.PlanView)

	applyCall := tools.Call{Tool: tools.ToolPersonalApps,
		Body: `{"operation":"change_apply","arguments":{"plan_id":"` + plan.PlanID + `"}}`}
	m.approvePersonalAppsCalls([]tools.Call{applyCall})

	if !m.personalAppsApprovals.ApprovedPlan(plan.PlanID, plan.Digest) {
		t.Fatal("approvePersonalAppsCalls did not record approval for the prepared plan")
	}
	if m.personalAppsApprovals.ApprovedPlan(plan.PlanID, "a-different-digest") {
		t.Fatal("approval matched a digest that was never prepared")
	}

	m.denyPersonalAppsCalls([]tools.Call{applyCall})
	if m.personalAppsApprovals.ApprovedPlan(plan.PlanID, plan.Digest) {
		t.Fatal("denyPersonalAppsCalls left the approval in place")
	}
}

// testMessageHandle mints a real message handle through a mail_search round
// trip against the fixture backend wired below, so tests build change sets
// out of host-issued handles like the model must.
func testMessageHandle(t *testing.T, m *Model) string {
	t.Helper()
	m.personalApps = nil // rebuild with a fixture Mail backend for this one test
	svc, err := personalapps.New(personalapps.Options{
		Scope: personalapps.Scope{MailEnabled: true, MutationsEnabled: true, AllowedAccounts: []string{"acct-1"}},
		Mail:  &fixtureMailBackend{},
		PrivacyGate: func(context.Context) error {
			m.personalAppsPrivate.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(personalapps.AdapterMail); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	m.personalApps = svc
	m.toolRunner.PersonalApps = svc

	res := svc.ExecuteRaw(context.Background(), []byte(`{"operation":"mail_search","arguments":{"account_ids":["`+accountHandle(t, svc)+`"]}}`))
	if res.Error != nil {
		t.Fatalf("mail_search failed: %s: %s", res.Error.Code, res.Error.Message)
	}
	messages := res.Data.(map[string]any)["messages"].([]personalapps.MessageSummaryView)
	if len(messages) == 0 {
		t.Fatal("fixture backend returned no messages")
	}
	return string(messages[0].ID)
}

func accountHandle(t *testing.T, svc *personalapps.Service) string {
	t.Helper()
	res := svc.ExecuteRaw(context.Background(), []byte(`{"operation":"mail_accounts"}`))
	if res.Error != nil {
		t.Fatalf("mail_accounts failed: %s: %s", res.Error.Code, res.Error.Message)
	}
	accounts := res.Data.(map[string]any)["accounts"].([]personalapps.AccountView)
	if len(accounts) == 0 {
		t.Fatal("fixture backend returned no accounts")
	}
	return string(accounts[0].ID)
}

// fixtureMailBackend is a minimal MailBackend for tests that need a real
// handle to build a change against.
type fixtureMailBackend struct{}

func (f *fixtureMailBackend) Accounts(context.Context) ([]personalapps.BackendAccount, error) {
	return []personalapps.BackendAccount{{
		Ref: personalapps.ResourceRef{Kind: personalapps.KindAccount, Adapter: personalapps.AdapterMail, AccountID: "acct-1"},
	}}, nil
}

func (f *fixtureMailBackend) Mailboxes(context.Context, personalapps.ResourceRef, personalapps.ResourceRef) ([]personalapps.BackendMailbox, error) {
	return nil, nil
}

func (f *fixtureMailBackend) Search(context.Context, personalapps.MailQuery) (personalapps.BackendMailPage, error) {
	ref := personalapps.ResourceRef{
		Kind: personalapps.KindMessage, Adapter: personalapps.AdapterMail,
		AccountID: "acct-1", ContainerPath: []string{"INBOX"}, NativeID: "m1",
	}
	return personalapps.BackendMailPage{
		Messages: []personalapps.BackendMessage{{Ref: ref, MailboxRef: personalapps.ResourceRef{
			Kind: personalapps.KindMailbox, Adapter: personalapps.AdapterMail, AccountID: "acct-1", ContainerPath: []string{"INBOX"},
		}, Subject: "hi"}},
		Scanned: 1, Complete: true,
	}, nil
}

func (f *fixtureMailBackend) Messages(context.Context, []personalapps.ResourceRef, int) ([]personalapps.BackendMessage, error) {
	return nil, nil
}

func TestPersonalAppsPrivateSessionBlocksSaveAndCache(t *testing.T) {
	m := personalAppsTestModel(t, nil)
	if m.personalAppsPrivate.Load() {
		t.Fatal("private session was already active before any read")
	}
	if err := m.enterPersonalAppsPrivateSession(context.Background()); err != nil {
		t.Fatalf("enterPersonalAppsPrivateSession: %v", err)
	}
	if !m.personalAppsPrivate.Load() {
		t.Fatal("enterPersonalAppsPrivateSession did not mark the session private")
	}

	if _, err := m.saveSession(false); err == nil {
		t.Fatal("saveSession succeeded during a private session")
	}

	// Disconnecting must not clear the marking.
	m.personalApps.Disconnect(personalapps.AdapterMail)
	if !m.personalAppsPrivate.Load() {
		t.Fatal("Disconnect cleared the private-session marking")
	}
	if _, err := m.saveSession(false); err == nil {
		t.Fatal("saveSession succeeded after disconnect during a private session")
	}
}

// TestSendToolResultsRecordsPersonalAppsResultForDebug guards a real
// diagnostic gap found live: a failed mutation's own outcomes/code/detail
// JSON — exactly what the model received — had no path into /debug last, so
// a real bridge/JXA failure (change_apply reporting outcome_unknown) was
// undiagnosable without asking the model to retype JSON from memory, which
// risks paraphrasing the very detail that matters.
func TestSendToolResultsRecordsPersonalAppsResultForDebug(t *testing.T) {
	m := newTestModel(t)
	output := `{"operation":"change_apply","outcomes":[{"outcome":"outcome_unknown","code":"internal","detail":"boom-detail"}]}`
	m.sendToolResults([]tools.Result{{
		Call:   tools.Call{ID: "call_1", Tool: tools.ToolPersonalApps},
		Output: output,
	}})
	if !strings.Contains(m.lastDebug.PersonalAppsResult, "boom-detail") {
		t.Fatalf("lastDebug.PersonalAppsResult = %q, want the tool's own output", m.lastDebug.PersonalAppsResult)
	}
}

func TestPersonalAppsDebugKeepsErrorDetailReadable(t *testing.T) {
	m := newTestModel(t)
	m.lastDebug.When = time.Now()
	m.lastDebug.PersonalAppsResult = untrusted.Frame("personal_apps", "change_apply", `{"operation":"change_apply","outcomes":[{"outcome":"outcome_unknown","code":"internal","detail":"Mail AppleEvent error -1728"}]}`)

	overlay := terminaltext.Sanitize(m.debugOverlay())
	for _, want := range []string{
		"personal_apps result\n",
		"\n  \"outcomes\": [",
		"\"detail\": \"Mail AppleEvent error -1728\"",
	} {
		if !strings.Contains(overlay, want) {
			t.Errorf("debug overlay missing %q:\n%s", want, overlay)
		}
	}
}

func TestTruncatePersonalAppsDebugResultKeepsTailDetail(t *testing.T) {
	value := strings.Repeat("x", 180) + `{"detail":"tail-error"}`
	got := truncatePersonalAppsDebugResult(value, 128)
	if !strings.Contains(got, "tail-error") {
		t.Fatalf("truncated diagnostic lost tail detail: %q", got)
	}
	if !strings.Contains(got, "showing beginning and end") {
		t.Fatalf("truncated diagnostic omitted its marker: %q", got)
	}
}
