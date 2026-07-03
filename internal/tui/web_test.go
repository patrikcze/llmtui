package tui

import (
	"testing"

	"github.com/patrikcze/llmtui/internal/tools"
)

func TestWebSpecsOnlySentWhenWebOn(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	if m.toolRunner == nil {
		t.Skip("no tool runner in this environment")
	}

	base := len(tools.Specs())
	req := m.buildRequest(nil)
	if len(req.Tools) != base {
		t.Errorf("web off: %d tool specs, want %d", len(req.Tools), base)
	}

	m.webOn = true
	req = m.buildRequest(nil)
	if len(req.Tools) != base+2 {
		t.Errorf("web on: %d tool specs, want %d", len(req.Tools), base+2)
	}
}

func TestCmdWebTogglesRunnerClient(t *testing.T) {
	m := newTestModel(t)
	if m.toolRunner == nil || m.webClient == nil {
		t.Skip("no tool runner in this environment")
	}
	if m.webOn || m.toolRunner.Web != nil {
		t.Fatal("web must start disabled by default")
	}

	cmdWeb(m, "on")
	if !m.webOn || m.toolRunner.Web == nil {
		t.Error("after /web on: webOn and runner client must be set")
	}

	cmdWeb(m, "off")
	if m.webOn || m.toolRunner.Web != nil {
		t.Error("after /web off: webOn and runner client must be cleared")
	}

	m.errText = ""
	cmdWeb(m, "bogus")
	if m.errText == "" {
		t.Error("unknown subcommand must set the usage error")
	}
}
