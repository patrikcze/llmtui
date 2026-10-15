package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

type nativeDiagnosticsProvider struct {
	requestCountingProvider
	native embedded.NativeDiagnostics
}

func (p *nativeDiagnosticsProvider) NativeDiagnostics() embedded.NativeDiagnostics { return p.native }

// /doctor names the loaded llama.cpp version and every ggml backend library
// that failed to open, since ggml itself silently falls back to the CPU. Both
// come from native code and the file system, so they are sanitized.
func TestDoctorReportShowsNativeVersionAndBackendLoadErrors(t *testing.T) {
	prov := &nativeDiagnosticsProvider{native: embedded.NativeDiagnostics{
		Loaded: true, Registrations: 3, Devices: 2,
		LlamaVersion:      "0.6.0",
		BackendLoadErrors: []string{"libggml-vulkan.so: libvulkan.so.1: cannot open\x1b[31m shared object file"},
	}}
	cfg := newTestModel(t).cfg

	report := doctorReport(prov, config.ProviderConfig{Type: "embedded"}, "model.gguf", cfg, 4096, "test")

	for _, want := range []string{"3 registered, 2 devices", "0.6.0", "libggml-vulkan.so: libvulkan.so.1: cannot open"} {
		if !strings.Contains(report, want) {
			t.Errorf("doctor report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "\x1b[31m") {
		t.Errorf("doctor report contains an unsanitized escape sequence:\n%q", report)
	}
}
