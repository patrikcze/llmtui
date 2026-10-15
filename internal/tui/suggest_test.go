package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/mcp"
)

func suggestionNames(m *Model) []string {
	names := make([]string, len(m.suggest.sugs))
	for i, c := range m.suggest.sugs {
		names[i] = c.name
	}
	return names
}

func argLabels(m *Model) []string {
	labels := make([]string, len(m.suggest.args))
	for i, a := range m.suggest.args {
		labels[i] = a.label
	}
	return labels
}

func TestBareSlashListsEveryCommandPinnedFirst(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/")

	visible := 0
	for _, c := range slashCommands() {
		if !c.hidden {
			visible++
		}
	}
	names := suggestionNames(m)
	if len(names) != visible {
		t.Fatalf("bare / lists %d commands, want all %d visible", len(names), visible)
	}
	if got := names[:len(pinnedCommands)]; strings.Join(got, ",") != strings.Join(pinnedCommands, ",") {
		t.Fatalf("popup starts with %v, want pinned %v", got, pinnedCommands)
	}
	if m.layout.suggestionRows != maxSuggestionRows {
		t.Fatalf("popup is %d rows, want %d", m.layout.suggestionRows, maxSuggestionRows)
	}
}

func TestExactCommandStillLeadsPinnedOnes(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/mo")
	if names := suggestionNames(m); len(names) < 2 || names[0] != "models" || names[1] != "model" {
		t.Fatalf("/mo = %v, want pinned models before model", names)
	}
	typeText(m, "del")
	if names := suggestionNames(m); names[0] != "model" {
		t.Fatalf("/model = %v, want the exact match first", names)
	}
}

func TestSuggestionArrowsScrollThroughAllCommands(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	typeText(m, "/")
	n := len(m.suggest.sugs)
	rows := m.layout.suggestionRows

	for range rows {
		m.Update(kDown())
	}
	if m.suggest.sugIdx != rows || m.suggest.sugTop != 1 {
		t.Fatalf("after %d downs: idx=%d top=%d, want idx=%d top=1", rows, m.suggest.sugIdx, m.suggest.sugTop, rows)
	}
	view := ansi.Strip(m.suggestionsView())
	if strings.Count(view, "\n")+1 != rows {
		t.Fatalf("popup shows %d rows, want %d:\n%s", strings.Count(view, "\n")+1, rows, view)
	}
	want := "▸ " + m.suggest.sugs[m.suggest.sugIdx].usage
	if !strings.Contains(view, want) {
		t.Fatalf("highlighted row %q not visible:\n%s", want, view)
	}
	// The highlight sits on the bottom row; one row up, both markers show.
	m.Update(kUp())
	if view := ansi.Strip(m.suggestionsView()); !strings.Contains(view, " ↑ ") || !strings.Contains(view, " ↓ ") {
		t.Fatalf("scrolled popup should mark rows above and below:\n%s", view)
	}

	// Up from the top wraps to the last command and scrolls to it.
	for m.suggest.sugIdx != 0 {
		m.Update(kUp())
	}
	m.Update(kUp())
	if m.suggest.sugIdx != n-1 || m.suggest.sugTop != n-rows {
		t.Fatalf("wrap: idx=%d top=%d, want idx=%d top=%d", m.suggest.sugIdx, m.suggest.sugTop, n-1, n-rows)
	}
	last := m.suggest.sugs[n-1].name
	if view := ansi.Strip(m.suggestionsView()); !strings.Contains(view, "▸ /"+last) {
		t.Fatalf("last command not visible after wrap:\n%s", view)
	}
	assertFrameFits(t, m)

	// Tab completes the scrolled-to command.
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.input.Value(); got != "/"+last+" " {
		t.Fatalf("tab completed to %q, want /%s ", got, last)
	}
}

func TestSuggestionWindow(t *testing.T) {
	cases := []struct {
		name                 string
		top, idx, n, rows, w int
	}{
		{"fits", 0, 3, 4, 6, 0},
		{"inside", 2, 4, 20, 6, 2},
		{"below", 0, 6, 20, 6, 1},
		{"above", 5, 2, 20, 6, 2},
		{"wrap to end", 0, 19, 20, 6, 14},
		{"no rows", 3, 4, 20, 0, 0},
		{"clamped", 18, 19, 20, 6, 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := suggestionWindow(tc.top, tc.idx, tc.n, tc.rows); got != tc.w {
				t.Fatalf("suggestionWindow(%d,%d,%d,%d) = %d, want %d", tc.top, tc.idx, tc.n, tc.rows, got, tc.w)
			}
		})
	}
}

func TestTabCompletesSubcommand(t *testing.T) {
	cases := []struct{ typed, want string }{
		{"/mcp l", "/mcp list "},
		{"/skills li", "/skills list "},
		{"/skill li", "/skill list "}, // an alias keeps what was typed
		{"/personal-apps connect c", "/personal-apps connect calendar "},
		{"/history export j", "/history export json "},
		{"/context strategy tr", "/context strategy truncate "},
	}
	for _, tc := range cases {
		t.Run(tc.typed, func(t *testing.T) {
			m := newTestModel(t)
			typeText(m, tc.typed)
			if len(m.suggest.args) == 0 {
				t.Fatalf("%q shows no argument suggestions", tc.typed)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
			if got := m.input.Value(); got != tc.want {
				t.Fatalf("tab completed %q to %q, want %q", tc.typed, got, tc.want)
			}
		})
	}
}

func TestTabAfterCommandOffersItsSubcommands(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/mc")
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.input.Value() != "/mcp " {
		t.Fatalf("tab = %q, want /mcp ", m.input.Value())
	}
	labels := argLabels(m)
	if len(labels) == 0 || labels[0] != "/mcp status" {
		t.Fatalf("/mcp offers %v", labels)
	}
	if !strings.Contains(ansi.Strip(m.render()), "/mcp connect <server>") {
		t.Fatal("argument popup is not rendered")
	}
	typeText(m, "list ")
	if len(m.suggest.args) != 0 {
		t.Fatalf("a completed subcommand should close the popup: %v", argLabels(m))
	}
}

func TestArgumentPopupHidesForFreeText(t *testing.T) {
	m := newTestModel(t)
	for _, typed := range []string{"/memory add remember this", "/tmp/notes are here", "/model some-model", "/nosuch x"} {
		m.input.Reset()
		typeText(m, typed)
		if m.suggest.count() != 0 {
			t.Fatalf("%q shows suggestions %v %v", typed, suggestionNames(m), argLabels(m))
		}
	}
}

func TestLiveArgumentValues(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{"ollama": {}, "lmstudio": {}, "bad name": {}, "evil\x1b[2J": {}}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "github", Transport: mcp.TransportStdio, Command: "x", Timeout: time.Second,
	}}, nil)

	typeText(m, "/provider switch ")
	if got := strings.Join(argLabels(m), ","); got != "/provider switch lmstudio,/provider switch ollama" {
		t.Fatalf("provider values = %q (names with spaces or escapes must be skipped)", got)
	}
	m.input.Reset()
	typeText(m, "/mcp connect g")
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.input.Value() != "/mcp connect github " {
		t.Fatalf("server completion = %q", m.input.Value())
	}
	if m.mcpRegistry.List()[0].Status == mcp.StatusConnected {
		t.Fatal("completing a server name must not connect it")
	}
}

func TestEnterRunsTypedTextUnlessArgumentWasPicked(t *testing.T) {
	m := newTestModel(t)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)

	// A typed prefix never expands into a subcommand: "/cache c" must not
	// become "/cache clear".
	typeText(m, "/cache c")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.errText, "usage: /cache") || strings.Contains(m.notice, "cleared") {
		t.Fatalf("/cache c ran something: err=%q notice=%q", m.errText, m.notice)
	}

	// Picking with the arrows and pressing Enter runs the picked phrase.
	m.errText = ""
	m.showReasoning = true
	typeText(m, "/thoughts ")
	m.Update(kDown()) // show → hide
	if m.suggest.args[m.suggest.sugIdx].label != "/thoughts hide" {
		t.Fatalf("highlighted %q", m.suggest.args[m.suggest.sugIdx].label)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.showReasoning || m.input.Value() != "" {
		t.Fatalf("picked /thoughts hide did not run: shown=%v input=%q", m.showReasoning, m.input.Value())
	}

	// A picked phrase that still needs an argument is filled in, not run.
	typeText(m, "/mcp ")
	for m.suggest.args[m.suggest.sugIdx].label != "/mcp inspect <server>" {
		m.Update(kDown())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "/mcp inspect " || m.overlayOpen {
		t.Fatalf("enter on inspect: input=%q overlay=%v", m.input.Value(), m.overlayOpen)
	}

	// Editing after picking drops the pick: Enter runs what is typed again.
	m.input.Reset()
	typeText(m, "/cache ")
	m.Update(kDown())
	typeText(m, "c")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if strings.Contains(m.notice, "cleared") {
		t.Fatal("an edit after picking must not keep the pick")
	}
}

func TestCompletableValue(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"github", true},
		{"local/skill-id", true},
		{"", false},
		{"two words", false},
		{"tab\tname", false},
		{"esc\x1b[2J", false},
		{"bell\a", false},
	}
	for _, tc := range cases {
		if got := completableValue(tc.v); got != tc.want {
			t.Errorf("completableValue(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestRemainingCommandsOpenAsDialogs(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "github", Transport: mcp.TransportStdio, Command: "x", Timeout: time.Second,
	}}, nil)
	cases := []struct {
		title string
		open  func()
	}{
		{"Response cache", func() { cmdCache(m, "") }},
		{"Model profile", func() { cmdProfile(m, "") }},
		{"Prompt", func() { cmdPrompt(m, "") }},
		{"Prompt · preview", func() { cmdPrompt(m, "preview") }},
		{"Prompt · raw", func() { cmdPrompt(m, "raw") }},
		{"Doctor · MCP", func() { cmdDoctor(m, "mcp") }},
		{"Doctor · personal apps", func() { cmdDoctor(m, "personal-apps") }},
		{"Doctor", func() { m.Update(doctorResultMsg{report: "provider ok"}) }},
		{"Help · keys", func() { cmdKeys(m, "help") }},
		{"Workspace tools · list", func() { cmdTools(m, "list") }},
		{"Tool · read_file", func() { cmdTools(m, "inspect read_file") }},
		{"Tools · command check", func() { cmdTools(m, "check ls") }},
		{"RAG", func() { cmdRag(m, "") }},
		{"RAG · sources", func() { cmdRag(m, "sources") }},
		{"MCP servers", func() { cmdMcp(m, "") }},
		{"MCP · tools", func() { cmdMcp(m, "tools") }},
		{"MCP · github", func() { cmdMcp(m, "inspect github") }},
		{"MCP", func() { cmdMcp(m, "inspect") }},
		{"Skills · status", func() { cmdSkills(m, "") }},
		{"Skill & plugin paths", func() { cmdSkills(m, "paths") }},
		{"Key inspector", func() { cmdKeys(m, "") }},
	}
	for _, tc := range cases {
		m.keys.keysMode = false
		m.closeOverlay()
		tc.open()
		if !m.modalActive() || m.modal.title != tc.title {
			t.Fatalf("%s did not open as a dialog (active=%v title=%q)", tc.title, m.modalActive(), m.modal.title)
		}
		assertFrameFits(t, m)
	}

	// The key inspector keeps logging keys inside its dialog.
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !m.modalActive() || !strings.Contains(ansi.Strip(m.viewport.View()+m.render()), "· q") {
		t.Fatal("key inspector did not log a key inside the dialog")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.overlayOpen || m.keys.keysMode || m.modal.title != "" {
		t.Fatal("esc should close the key inspector dialog")
	}
}
