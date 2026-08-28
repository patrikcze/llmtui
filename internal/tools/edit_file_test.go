package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestEditFileSingleExactMatch(t *testing.T) {
	root := t.TempDir()
	original := "package x\n\nconst Timeout = 30 // seconds\n\nfunc f() {}\n"
	writeTemp(t, root, "x.go", original)

	res := NewRunner(root, 64).Execute(Call{
		Tool: ToolEditFile, Path: "x.go",
		OldText: "const Timeout = 30 // seconds", NewText: "const Timeout = 60 // seconds",
	})
	if res.Err != nil {
		t.Fatalf("edit failed: %v", res.Err)
	}
	if !strings.Contains(res.Output, "replaced 1 exact occurrence") {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Diff == "" {
		t.Fatal("expected a display diff")
	}
	got, _ := os.ReadFile(filepath.Join(root, "x.go"))
	want := strings.Replace(original, "= 30 //", "= 60 //", 1)
	if string(got) != want {
		t.Fatalf("file =\n%q\nwant\n%q", got, want)
	}
}

func TestEditFileEmptyNewTextDeletes(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "x.txt", "keep 1\nobsolete line\nkeep 2\n")
	res := NewRunner(root, 64).Execute(Call{
		Tool: ToolEditFile, Path: "x.txt", OldText: "obsolete line\n", NewText: "",
	})
	if res.Err != nil {
		t.Fatalf("edit failed: %v", res.Err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "x.txt"))
	if string(got) != "keep 1\nkeep 2\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditFileFailuresLeaveFileUnchanged(t *testing.T) {
	root := t.TempDir()
	const original = "alpha\nbeta\nalpha\n"
	r := NewRunner(root, 64)

	cases := []struct {
		name       string
		call       Call
		wantErrSub string
	}{
		{"zero matches", Call{Tool: ToolEditFile, Path: "f.txt", OldText: "gamma", NewText: "x"}, "not found"},
		{"multiple matches", Call{Tool: ToolEditFile, Path: "f.txt", OldText: "alpha", NewText: "x"}, "matches 2"},
		{"empty old_text", Call{Tool: ToolEditFile, Path: "f.txt", OldText: "", NewText: "x"}, "old_text"},
		{"old == new", Call{Tool: ToolEditFile, Path: "f.txt", OldText: "beta", NewText: "beta"}, "identical"},
		{"missing file", Call{Tool: ToolEditFile, Path: "nope.txt", OldText: "a", NewText: "b"}, "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTemp(t, root, "f.txt", original)
			res := r.Execute(tc.call)
			if res.Err == nil {
				t.Fatalf("want error containing %q", tc.wantErrSub)
			}
			if !strings.Contains(res.Err.Error(), tc.wantErrSub) {
				t.Fatalf("err = %v, want it to contain %q", res.Err, tc.wantErrSub)
			}
			if tc.call.Path == "f.txt" {
				got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
				if string(got) != original {
					t.Fatalf("failed edit modified the file: %q", got)
				}
			}
		})
	}
}

func TestEditFileNeverCreatesFile(t *testing.T) {
	root := t.TempDir()
	res := NewRunner(root, 64).Execute(Call{Tool: ToolEditFile, Path: "new.go", OldText: "a", NewText: "b"})
	if res.Err == nil {
		t.Fatal("edit of a missing file should fail")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("edit_file must not create the file")
	}
}

func TestEditFileRejectsDirectoryAndEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	for _, tc := range []struct{ name, path string }{
		{"directory", "d"},
		{"parent escape", "../x"},
		{"absolute", "/etc/hosts"},
	} {
		if res := r.Execute(Call{Tool: ToolEditFile, Path: tc.path, OldText: "a", NewText: "b"}); res.Err == nil {
			t.Errorf("%s: edit should be rejected", tc.name)
		}
	}
}

func TestEditFileBlockedWritePathsStayBlocked(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, root, ".git/config", "[core]\n")
	res := NewRunner(root, 64).Execute(Call{
		Tool: ToolEditFile, Path: ".git/config", OldText: "[core]\n", NewText: "[evil]\n",
	})
	if res.Err == nil {
		t.Fatal("edit_file into .git must be blocked")
	}
}

func TestEditFileSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("password=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	res := NewRunner(root, 64).Execute(Call{
		Tool: ToolEditFile, Path: "link.txt", OldText: "hunter2", NewText: "cracked",
	})
	if res.Err == nil {
		t.Fatal("edit through a workspace-escaping symlink must be rejected")
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "password=hunter2\n" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

func TestEditFileResultExceedsWriteLimit(t *testing.T) {
	root := t.TempDir()
	// 1 KB runner cap. A file just under it, an edit that pushes it over.
	writeTemp(t, root, "f.txt", strings.Repeat("a", 900)+"MARK")
	res := NewRunner(root, 1).Execute(Call{
		Tool: ToolEditFile, Path: "f.txt", OldText: "MARK", NewText: strings.Repeat("b", 200),
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "write limit") {
		t.Fatalf("err = %v, want a write-limit error", res.Err)
	}
}

func TestEditFileSourceExceedsEditLimit(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "big.txt", strings.Repeat("x", 4000)) // > 1 KB cap
	res := NewRunner(root, 1).Execute(Call{
		Tool: ToolEditFile, Path: "big.txt", OldText: "x", NewText: "y",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "edit limit") {
		t.Fatalf("err = %v, want an edit-limit error", res.Err)
	}
}

func TestEditFileRejectsNonUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.bin"), []byte{0x00, 0xff, 0xfe, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	res := NewRunner(root, 64).Execute(Call{Tool: ToolEditFile, Path: "b.bin", OldText: "x", NewText: "y"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "UTF-8") {
		t.Fatalf("err = %v, want a UTF-8 rejection", res.Err)
	}
}

func TestEditFileStaleContentGuard(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	writeTemp(t, root, "f.txt", "one\nTWO\nthree\n")
	r := NewRunner(root, 64)

	// writeFileChecked directly: the precondition must reject a mismatch.
	diff, err := r.writeFileChecked("f.txt", "whatever", ptr("a different snapshot"))
	if err == nil || diff != "" {
		t.Fatalf("stale precondition did not fail: diff=%q err=%v", diff, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO\nthree\n" {
		t.Fatalf("file changed despite a failed precondition: %q", got)
	}

	// Matching snapshot succeeds.
	if _, err := r.writeFileChecked("f.txt", "one\n2\nthree\n", ptr("one\nTWO\nthree\n")); err != nil {
		t.Fatalf("matching precondition failed: %v", err)
	}
}

func ptr(s string) *string { return &s }

func TestEditFileNativeDecoding(t *testing.T) {
	got := CallsFromNative([]provider.ToolCall{
		{ID: "e1", Name: ToolEditFile, Arguments: `{"path":"a.go","old_text":"foo","new_text":"bar"}`},
	})
	want := Call{ID: "e1", Tool: ToolEditFile, Path: "a.go", OldText: "foo", NewText: "bar"}
	if got[0] != want {
		t.Fatalf("call = %+v, want %+v", got[0], want)
	}

	bad := CallsFromNative([]provider.ToolCall{
		{ID: "e2", Name: ToolEditFile, Arguments: `{"path":"a.go","old_text":"","new_text":"bar"}`},
	})
	if bad[0].InputErr == "" {
		t.Fatal("empty old_text should be an InputErr")
	}
}

func TestEditFileFencedDecoding(t *testing.T) {
	calls := Parse("```tool edit_file internal/config/config.go\n" +
		`{"old_text":"Timeout: 30 * time.Second,","new_text":"Timeout: 60 * time.Second,"}` + "\n```")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.Path != "internal/config/config.go" || c.OldText != "Timeout: 30 * time.Second," ||
		c.NewText != "Timeout: 60 * time.Second," || c.Body != "" || c.InputErr != "" {
		t.Fatalf("call = %+v", c)
	}

	// Missing path in the info string is a recoverable error.
	noPath := Parse("```tool edit_file\n{\"old_text\":\"a\",\"new_text\":\"b\"}\n```")
	if len(noPath) != 1 || noPath[0].InputErr == "" {
		t.Fatalf("call = %+v", noPath[0])
	}
}

func TestEditFileDescribeHidesContent(t *testing.T) {
	c := Call{Tool: ToolEditFile, Path: "secrets.go", OldText: "APIKEY=abc123", NewText: "APIKEY=xyz789"}
	d := c.Describe()
	if strings.Contains(d, "abc123") || strings.Contains(d, "xyz789") {
		t.Fatalf("Describe leaked replacement content: %q", d)
	}
	if !strings.Contains(d, "secrets.go") {
		t.Fatalf("Describe = %q, want it to name the file", d)
	}
}
