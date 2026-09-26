package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestRuntimeContextPlacementIsLimitedToEmbeddedNativeContinuations(t *testing.T) {
	for _, tc := range []struct {
		name, providerType             string
		native, continuation, deferred bool
	}{
		{name: "embedded continuation", providerType: "embedded", native: true, continuation: true, deferred: true},
		{name: "fresh embedded chat", providerType: "embedded", native: true},
		{name: "fenced continuation", providerType: "embedded", continuation: true},
		{name: "remote continuation", providerType: "openai", native: true, continuation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.Providers = map[string]config.ProviderConfig{m.cfg.Provider: {Type: tc.providerType}}
			m.toolsOn, m.toolsNative = true, tc.native
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			m.toolRecoveryFeedback = "schema recovery diagnostic"
			base := m.compositionBase("original user", nil, tc.continuation)
			if base.input.RuntimeContextAfterHistory != tc.deferred {
				t.Fatalf("deferred=%v; want %v", base.input.RuntimeContextAfterHistory, tc.deferred)
			}
			out := composeFromBase(base, nil, "")
			inSystem := strings.Contains(out.Messages[0].Content, m.toolRecoveryFeedback)
			if inSystem == tc.deferred {
				t.Fatal("tool recovery feedback has incorrect system placement")
			}
			if tc.deferred && !strings.Contains(out.Messages[len(out.Messages)-1].Content, m.toolRecoveryFeedback) {
				t.Fatal("recovery feedback was lost instead of deferred")
			}
		})
	}
}
