package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobFilesRecursiveAndConfined(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "main.go", "package main\n")
	writeSearchFixture(t, root, "internal/nested/tool.go", "package nested\n")
	writeSearchFixture(t, root, "internal/notes.md", "notes\n")
	writeSearchFixture(t, root, ".git/hidden.go", "package hidden\n")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolGlob, Body: "**/*.go"})
	if res.Err != nil {
		t.Fatalf("glob: %v", res.Err)
	}
	for _, want := range []string{"main.go", "internal/nested/tool.go"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("glob output missing %q: %q", want, res.Output)
		}
	}
	for _, unwanted := range []string{"notes.md", ".git/hidden.go"} {
		if strings.Contains(res.Output, unwanted) {
			t.Errorf("glob output contains %q: %q", unwanted, res.Output)
		}
	}
	if res := r.Execute(Call{Tool: ToolGlob, Path: "../outside", Body: "*"}); res.Err == nil {
		t.Fatal("glob escaped the workspace")
	}
}

func TestGrepFilesRegexFilterAndSecretPolicy(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "main.go", "package main\n// TODO: first\n")
	writeSearchFixture(t, root, "internal/tool.go", "package internal\n// FIXME: second\n")
	writeSearchFixture(t, root, "README.md", "TODO: documentation\n")
	writeSearchFixture(t, root, ".env", "API_TOKEN=TODO-secret\n")
	writeSearchFixture(t, root, ".git/config", "TODO: metadata\n")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolGrep, Body: "TODO|FIXME", Filter: "**/*.go"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	for _, want := range []string{"main.go:2:// TODO: first", "internal/tool.go:2:// FIXME: second"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("grep output missing %q: %q", want, res.Output)
		}
	}
	for _, unwanted := range []string{"README.md", "TODO-secret", ".git/config"} {
		if strings.Contains(res.Output, unwanted) {
			t.Errorf("grep output contains %q: %q", unwanted, res.Output)
		}
	}

	secretCall := Call{Tool: ToolGrep, Path: ".env", Body: "TODO"}
	if !r.NeedsApproval(secretCall) {
		t.Fatal("grep of an explicit secret file should require approval")
	}
	if res := r.Execute(secretCall); res.Err != nil || !strings.Contains(res.Output, "TODO-secret") {
		t.Fatalf("approved explicit secret grep = %q, err=%v", res.Output, res.Err)
	}
	if res := r.Execute(Call{Tool: ToolGrep, Body: "["}); res.Err == nil {
		t.Fatal("invalid grep regular expression succeeded")
	}
}

func writeSearchFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
