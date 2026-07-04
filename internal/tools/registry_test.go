package tools

import (
	"testing"
)

func TestDefaultRegistryContainsAllBuiltins(t *testing.T) {
	reg := DefaultRegistry()
	required := []string{ToolListDir, ToolReadFile, ToolWriteFile, ToolRunCommand, ToolWebSearch, ToolWebFetch}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing capability %q", name)
		}
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	info := CapabilityInfo{Name: "test_tool", Source: "builtin", Safety: SafetyReadOnly}
	if err := r.Register(info); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(info); err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(CapabilityInfo{Source: "builtin"}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestListOrderMatchesRegistration(t *testing.T) {
	r := NewRegistry()
	names := []string{"alpha", "beta", "gamma"}
	for _, n := range names {
		_ = r.Register(CapabilityInfo{Name: n, Source: "builtin", Safety: SafetyReadOnly})
	}
	got := r.List()
	if len(got) != len(names) {
		t.Fatalf("List() len = %d, want %d", len(got), len(names))
	}
	for i, info := range got {
		if info.Name != names[i] {
			t.Errorf("List()[%d] = %q, want %q", i, info.Name, names[i])
		}
	}
}

func TestEnabledListFiltersOnSource(t *testing.T) {
	reg := DefaultRegistry()
	// Only builtin enabled, web off.
	sources := map[string]bool{"builtin": true, "web": false}
	caps := reg.EnabledList(sources)
	for _, c := range caps {
		if c.Source != "builtin" {
			t.Errorf("EnabledList returned %q (source=%q) when web was off", c.Name, c.Source)
		}
	}
	// Verify builtin tools are present.
	found := map[string]bool{}
	for _, c := range caps {
		found[c.Name] = true
	}
	for _, name := range []string{ToolListDir, ToolReadFile, ToolWriteFile, ToolRunCommand} {
		if !found[name] {
			t.Errorf("builtin tool %q missing from EnabledList", name)
		}
	}
}

func TestBuiltinSafetyClasses(t *testing.T) {
	reg := DefaultRegistry()
	cases := map[string]SafetyClass{
		ToolListDir:    SafetyReadOnly,
		ToolReadFile:   SafetyReadOnly,
		ToolWriteFile:  SafetyWorkspaceWrite,
		ToolRunCommand: SafetyCommand,
		ToolWebSearch:  SafetyNetwork,
		ToolWebFetch:   SafetyNetwork,
	}
	for name, want := range cases {
		info, ok := reg.Get(name)
		if !ok {
			t.Errorf("tool %q not found", name)
			continue
		}
		if info.Safety != want {
			t.Errorf("tool %q safety = %q, want %q", name, info.Safety, want)
		}
	}
}
