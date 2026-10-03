package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/config"
)

func TestHistoryDialogEnterLoadsSession(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("remember this")
	m.saveWithNotice()
	saved := m.sessionName
	m.session.Clear()

	cmdHistory(m, "")
	if !m.modalActive() || m.picker.pickerKind != pickerHistory {
		t.Fatal("/history did not open the sessions dialog")
	}
	if !strings.Contains(ansi.Strip(m.render()), "Saved sessions") {
		t.Fatal("dialog title missing")
	}
	assertFrameFits(t, m)
	m.picker.pickerIdx = selectedIndex(m.picker.pickerItems, saved)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.overlayOpen || len(m.session.Messages) == 0 || !strings.Contains(m.notice, "loaded") {
		t.Fatalf("enter did not load the session: overlay=%v msgs=%d notice=%q", m.overlayOpen, len(m.session.Messages), m.notice)
	}
}

func TestTemplateDialogUsesAndClearsTemplate(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Templates = map[string]config.TemplateConfig{
		"golang": {Description: "Go reviewer", PromptMode: "coding", Temperature: 0.2},
		"python": {Description: "Python helper", PromptMode: "balanced", Temperature: 0.5},
	}
	cmdTemplate(m, "list")
	if !m.modalActive() || m.picker.pickerKind != pickerTemplate {
		t.Fatal("/template list did not open the templates dialog")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.template != "golang" {
		t.Fatalf("template = %q, want golang", m.template)
	}
	cmdTemplate(m, "list")
	if m.picker.pickerItems[m.picker.pickerIdx] != "golang" {
		t.Fatal("the active template must be preselected")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.template != "" {
		t.Fatalf("enter on the active template should clear it, got %q", m.template)
	}
}

func TestPersonalAppsDialogConnectsAndDisconnects(t *testing.T) {
	m := personalAppsTestModel(t, func(c *config.PersonalAppsConfig) {
		c.Mail.AllowedAccounts = []string{"very-secret-account-id"}
	})
	cmdPersonalApps(m, "")
	if !m.modalActive() || m.picker.pickerKind != pickerPersonalApps {
		t.Fatal("/personal-apps did not open the dialog")
	}
	frame := ansi.Strip(m.render())
	if strings.Contains(frame, "very-secret-account-id") {
		t.Fatal("the dialog must not name accounts")
	}
	if !strings.Contains(frame, "not connected") {
		t.Fatalf("mail state missing:\n%s", frame)
	}
	m.picker.pickerIdx = 0 // Mail
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.personalApps.Connection().MailConnected {
		t.Fatal("enter on Mail did not connect it")
	}
	if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "connected") {
		t.Fatal("the dialog should stay open and show the new state")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.personalApps.Connection().MailConnected {
		t.Fatal("enter on a connected Mail did not disconnect it")
	}
	m.picker.pickerIdx = 1 // Calendar, disabled in config
	m.errText = ""
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.errText == "" || m.personalApps.Connection().CalendarConnected {
		t.Fatal("a disabled Calendar must not connect")
	}
}

func TestSkillsPluginsAndEntitiesOpenAsDialogs(t *testing.T) {
	m := newTestModel(t)
	setupSkills(t, m, map[string]string{"alpha": "Alpha instructions."})
	cmdSkills(m, "list")
	if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "Skills") {
		t.Fatal("/skills list did not open as a dialog")
	}
	m.closeOverlay()
	cmdPlugins(m, "")
	if !m.modalActive() || m.picker.pickerKind != pickerPlugin {
		t.Fatal("bare /plugins should open the selectable plugins dialog")
	}
	m.closeOverlay()
	cmdEntities(m, "list")
	if !m.modalActive() || m.picker.pickerKind != pickerEntity {
		t.Fatal("/entities list did not open the entities dialog")
	}
}

func TestTextDialogsWrapToTheDialogAndFit(t *testing.T) {
	m := newTestModel(t)
	m.resize(90, 30)
	for _, open := range []func(){
		func() {
			m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			typeText(m, "/help")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		},
		func() { m.closeOverlay(); cmdDebug(m, "last") },
		func() { m.closeOverlay(); cmdEntities(m, "status") },
	} {
		open()
		if !m.modalActive() {
			t.Fatal("text command did not open as a dialog")
		}
		for i, line := range strings.Split(m.viewport.View(), "\n") {
			if w := ansi.StringWidth(line); w > m.modal.innerW {
				t.Fatalf("body line %d is %d wide, dialog body is %d: %q", i, w, m.modal.innerW, ansi.Strip(line))
			}
		}
		assertFrameFits(t, m)
	}
}

func TestDialogTextIsSelectable(t *testing.T) {
	m := newTestModel(t)
	cmdDebug(m, "last")
	if m.selectionZoneID() != modalBodyZoneID {
		t.Fatal("selection must map through the dialog body while it is open")
	}
	z := renderedZone(t, m, modalBodyZoneID)
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX + 8, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: z.StartX + 8, Y: z.StartY, Button: tea.MouseLeft})
	if !m.sel.hasSelection {
		t.Fatal("dragging inside the dialog did not select text")
	}
}
