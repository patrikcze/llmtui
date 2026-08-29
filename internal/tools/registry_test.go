package tools

import (
	"testing"
)

func TestDefaultRegistryContainsAllBuiltins(t *testing.T) {
	reg := DefaultRegistry()
	required := []string{ToolListDir, ToolReadFile, ToolGlob, ToolGrep, ToolWriteFile, ToolEditFile, ToolRunCommand, ToolWebSearch, ToolWebFetch}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing capability %q", name)
		}
	}
}

func TestEditFileRegisteredAsWorkspaceWriteWithApproval(t *testing.T) {
	info, ok := DefaultRegistry().Get(ToolEditFile)
	if !ok {
		t.Fatal("edit_file not registered")
	}
	if info.Source != "builtin" || info.Safety != SafetyWorkspaceWrite || info.Approval != "ask" {
		t.Fatalf("edit_file capability = %+v", info)
	}
}

func TestDefaultRegistryApprovalMatchesRunnerPolicy(t *testing.T) {
	type approvalCheck struct {
		call Call
		want bool
	}
	type approvalCase struct {
		name     string
		approval string
		checks   []approvalCheck
	}

	runner := NewRunner(t.TempDir(), 64)
	cases := []approvalCase{
		{
			name:     ToolListDir,
			approval: "no",
			checks:   []approvalCheck{{call: Call{Tool: ToolListDir}}},
		},
		{
			name:     ToolReadFile,
			approval: "ask for secret files",
			checks: []approvalCheck{
				{call: Call{Tool: ToolReadFile, Path: "main.go"}},
				{call: Call{Tool: ToolReadFile, Path: ".env"}, want: true},
			},
		},
		{
			name:     ToolGlob,
			approval: "no",
			checks:   []approvalCheck{{call: Call{Tool: ToolGlob, Body: "*.go"}}},
		},
		{
			name:     ToolGrep,
			approval: "ask for an explicit secret file",
			checks: []approvalCheck{
				{call: Call{Tool: ToolGrep, Path: "main.go", Body: "TODO"}},
				{call: Call{Tool: ToolGrep, Path: ".env", Body: "TOKEN"}, want: true},
			},
		},
		{
			name:     ToolWriteFile,
			approval: "ask",
			checks:   []approvalCheck{{call: Call{Tool: ToolWriteFile, Path: "main.go", Body: "package main"}, want: true}},
		},
		{
			name:     ToolEditFile,
			approval: "ask",
			checks:   []approvalCheck{{call: Call{Tool: ToolEditFile, Path: "main.go", OldText: "old", NewText: "new"}, want: true}},
		},
		{
			name:     ToolRunCommand,
			approval: "ask unless read-only",
			checks: []approvalCheck{
				{call: Call{Tool: ToolRunCommand, Body: "go list ./..."}},
				{call: Call{Tool: ToolRunCommand, Body: "go test ./..."}, want: true},
			},
		},
		{
			name:     ToolWebSearch,
			approval: "no for ordinary queries; ask for bulk or opaque ones",
			checks: []approvalCheck{
				{call: Call{Tool: ToolWebSearch, Body: "go context cancellation"}},
				{call: Call{Tool: ToolWebSearch, Body: "search\nterm"}, want: true},
			},
		},
		{
			name:     ToolWebFetch,
			approval: "ask",
			checks:   []approvalCheck{{call: Call{Tool: ToolWebFetch, Path: "https://example.com"}, want: true}},
		},
		{
			name:     ToolSkillLoad,
			approval: "no",
			checks:   []approvalCheck{{call: Call{Tool: ToolSkillLoad, Path: "go-review"}}},
		},
		{
			name:     ToolAskUser,
			approval: "no (never authorizes another tool)",
			checks:   []approvalCheck{{call: Call{Tool: ToolAskUser}}},
		},
		{
			name:     ToolLocalContext,
			approval: "ask for clipboard; otherwise no",
			checks: []approvalCheck{
				{call: Call{Tool: ToolLocalContext, ContextKind: LocalContextTime}},
				{call: Call{Tool: ToolLocalContext, ContextKind: LocalContextClipboard}, want: true},
			},
		},
		{
			name:     ToolSearch,
			approval: "no (discovery only)",
			checks:   []approvalCheck{{call: Call{Tool: ToolSearch, Body: "search tools"}}},
		},
	}

	registry := DefaultRegistry()
	if got, want := len(registry.List()), len(cases); got != want {
		t.Fatalf("registry lists %d tools, test covers %d", got, want)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := registry.Get(tc.name)
			if !ok {
				t.Fatalf("registry missing %q", tc.name)
			}
			if info.Approval != tc.approval {
				t.Errorf("registry approval = %q, want %q", info.Approval, tc.approval)
			}
			for _, check := range tc.checks {
				if got := runner.NeedsApproval(check.call); got != check.want {
					t.Errorf("NeedsApproval(%+v) = %v, want %v", check.call, got, check.want)
				}
			}
		})
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
	for _, name := range []string{ToolListDir, ToolReadFile, ToolGlob, ToolGrep, ToolWriteFile, ToolRunCommand} {
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
		ToolGlob:       SafetyReadOnly,
		ToolGrep:       SafetyReadOnly,
		ToolWriteFile:  SafetyWorkspaceWrite,
		ToolEditFile:   SafetyWorkspaceWrite,
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
