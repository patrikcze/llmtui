package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/tools"
)

func assertFrameFits(t *testing.T, m *Model) {
	t.Helper()
	for i, line := range strings.Split(m.render(), "\n") {
		if w := ansi.StringWidth(line); w > m.width {
			t.Fatalf("rendered line %d is %d cells wide, terminal is %d: %q", i, w, m.width, ansi.Strip(line))
		}
	}
}

func TestProvidersDialogOpensAsModalAndRestoresViewport(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 20}} {
		m := newTestModel(t)
		m.resize(size[0], size[1])
		m.cfg.UI.ProviderProbe = false
		m.cfg.Providers = map[string]config.ProviderConfig{
			m.prov.Name(): {Type: "mock"},
			"lmstudio":    {Type: "openai_compatible", BaseURL: "http://user:secret@localhost:1234/v1"},
		}
		fullW, fullH := m.viewport.Width(), m.viewport.Height()
		if cmd := m.openProvidersPicker(); cmd != nil {
			t.Fatal("provider checks must not run with ui.provider_probe false")
		}
		if !m.modalActive() || m.viewport.Width() >= fullW {
			t.Fatalf("%dx%d: providers did not open as a dialog (viewport %d, full %d)", size[0], size[1], m.viewport.Width(), fullW)
		}
		assertFrameFits(t, m)
		frame := ansi.Strip(m.render())
		if !strings.Contains(frame, "Providers") || !strings.Contains(frame, "lmstudio") {
			t.Fatalf("dialog content missing:\n%s", frame)
		}
		if strings.Contains(frame, "secret") {
			t.Fatal("endpoint credentials must never be displayed")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.modalActive() || m.viewport.Width() != fullW || m.viewport.Height() != fullH {
			t.Fatalf("closing the dialog did not restore the viewport: %dx%d, want %dx%d", m.viewport.Width(), m.viewport.Height(), fullW, fullH)
		}
	}
}

func TestDialogFallsBackToFullAreaOnTinyTerminal(t *testing.T) {
	m := newTestModel(t)
	m.resize(44, 20)
	m.cfg.UI.ProviderProbe = false
	m.openProvidersPicker()
	if m.modalActive() || !m.overlayOpen {
		t.Fatal("a tiny terminal must fall back to the full-area overlay")
	}
}

func TestToolsAndProfilesOpenAsDialogs(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	cmdTools(m, "")
	if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "Workspace tools") {
		t.Fatal("/tools did not open as a dialog")
	}
	assertFrameFits(t, m)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m.openProfilesPicker()
	if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "Model profiles") {
		t.Fatal("/profile list did not open as a dialog")
	}
	assertFrameFits(t, m)
}

func TestPendingAskTakesOverOpenDialog(t *testing.T) {
	m := newTestModel(t)
	m.cfg.UI.ProviderProbe = false
	m.openProvidersPicker()
	fullW := m.width
	m.pauseForAskUser(tools.Call{ID: "ask-1", Tool: tools.ToolAskUser, Body: `{"question":"Which file?"}`})
	if m.overlayOpen || m.modalActive() {
		t.Fatal("a pending question must close the dialog so the next key answers it")
	}
	if m.viewport.Width() != fullW {
		t.Fatalf("viewport width = %d after the dialog yielded, want %d", m.viewport.Width(), fullW)
	}
}

func TestProviderProbesReportReachabilityAndDropStaleResults(t *testing.T) {
	saved := providerProbeTimeout
	providerProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { providerProbeTimeout = saved })

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"google/gemma-4-e4b"},{"id":"openai/gpt-oss-20b"}]}`))
	}))
	t.Cleanup(healthy.Close)
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hanging.Close)

	m := newTestModel(t)
	m.cfg.UI.ProviderProbe = true
	m.cfg.Providers = map[string]config.ProviderConfig{
		"up":   {Type: "openai_compatible", BaseURL: healthy.URL + "/v1"},
		"down": {Type: "openai_compatible", BaseURL: hanging.URL + "/v1"},
	}
	cmd := m.openProvidersPicker()
	if cmd == nil {
		t.Fatal("expected provider checks")
	}
	staleGen := m.probes.gen
	started := time.Now()
	for _, msg := range runBatch(cmd) {
		m.Update(msg)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("checks took %s; the timeout must bound an unreachable provider", elapsed)
	}
	if got := m.probes.results["up"]; got.status != probeOK || len(got.models) != 2 {
		t.Fatalf("up = %+v, want online with 2 models", got)
	}
	if got := m.probes.results["down"]; got.status != probeFailed {
		t.Fatalf("down = %+v, want offline", got)
	}
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "2 models") || !strings.Contains(frame, "offline") {
		t.Fatalf("status not shown:\n%s", frame)
	}

	m.Update(tea.KeyPressMsg{Text: "r", Code: 'r'}) // recheck starts a new generation
	m.handleProviderProbe(providerProbeMsg{gen: staleGen, name: "up", result: providerProbeResult{status: probeFailed}})
	if m.probes.results["up"].status == probeFailed {
		t.Fatal("a result from an earlier generation must be ignored")
	}
}

// runBatch executes cmd and every command nested in tea.BatchMsg results,
// returning the leaf messages.
func runBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runBatch(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}
