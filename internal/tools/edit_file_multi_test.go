package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func editCall(path string, dryRun bool, pairs ...EditPair) Call {
	c := Call{Tool: ToolEditFile, Path: path, DryRun: dryRun}
	if err := setEditPairs(&c, "", "", pairs); err != nil {
		panic(err)
	}
	return c
}

func readFixture(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditFileMultipleEditsApplyInOrder(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.go", "alpha\nbeta\ngamma\n")
	r := NewRunner(root, 64)

	res := r.Execute(editCall("a.go", false,
		EditPair{OldText: "alpha", NewText: "ALPHA"},
		EditPair{OldText: "ALPHA\nbeta", NewText: "ALPHA\nBETA"}, // sees the first edit's result
	))
	if res.Err != nil {
		t.Fatalf("edit_file: %v", res.Err)
	}
	if got := readFixture(t, root, "a.go"); got != "ALPHA\nBETA\ngamma\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(res.Output, "applied 2 exact replacements") || res.Meta.Effect != EffectChanged {
		t.Fatalf("output %q meta %+v", res.Output, res.Meta)
	}
}

func TestEditFileMultipleEditsAreAllOrNothing(t *testing.T) {
	tests := []struct {
		name  string
		pairs []EditPair
		code  string
	}{
		{"second missing", []EditPair{{OldText: "alpha", NewText: "A"}, {OldText: "nope", NewText: "B"}}, "match_not_found"},
		{"second ambiguous", []EditPair{{OldText: "alpha", NewText: "x"}, {OldText: "x", NewText: "y"}}, "ambiguous_match"},
		{"edits cancel out", []EditPair{{OldText: "alpha", NewText: "tmp"}, {OldText: "tmp", NewText: "alpha"}}, "no_change"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSearchFixture(t, root, "a.go", "alpha\nx\n")
			res := NewRunner(root, 64).Execute(editCall("a.go", false, tc.pairs...))
			if res.Err == nil || res.Meta.Error == nil || res.Meta.Error.Code != tc.code {
				t.Fatalf("err = %v meta = %+v, want code %s", res.Err, res.Meta.Error, tc.code)
			}
			if got := readFixture(t, root, "a.go"); got != "alpha\nx\n" {
				t.Fatalf("file changed after a failed multi-edit: %q", got)
			}
		})
	}
}

func TestEditFileDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.go", "one\ntwo\nthree\n")
	r := NewRunner(root, 64)
	res := r.Execute(editCall("a.go", true, EditPair{OldText: "two", NewText: "TWO"}))
	if res.Err != nil {
		t.Fatalf("dry run: %v", res.Err)
	}
	if got := readFixture(t, root, "a.go"); got != "one\ntwo\nthree\n" {
		t.Fatalf("dry run wrote the file: %q", got)
	}
	for _, want := range []string{"dry run", "--- a/a.go", "+++ b/a.go", "@@ -1,3 +1,3 @@", "-two", "+TWO", " one"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, res.Output)
		}
	}
	if res.Meta.Effect != EffectNone || res.Diff != "" {
		t.Fatalf("dry run meta = %+v diff = %q", res.Meta, res.Diff)
	}
}

// Workspace Tool Safety Invariant: a dry run still goes through the write
// guardrails and confinement, and a dry run of a secret file asks like a
// read of it would.
func TestEditFileDryRunKeepsGuardrails(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".git/config", "a\n")
	r := NewRunner(root, 64)
	if res := r.Execute(editCall(".git/config", true, EditPair{OldText: "a", NewText: "b"})); res.Err == nil {
		t.Fatal("dry run inside .git must be refused")
	}
	if res := r.Execute(editCall("../x", true, EditPair{OldText: "a", NewText: "b"})); res.Err == nil {
		t.Fatal("dry run outside the workspace must be refused")
	}
	if !r.NeedsApproval(editCall(".env", true, EditPair{OldText: "a", NewText: "b"})) {
		t.Fatal("a dry run of a secret file must ask")
	}
	if r.NeedsApproval(editCall("main.go", true, EditPair{OldText: "a", NewText: "b"})) {
		t.Fatal("a dry run of an ordinary file should not ask")
	}
	if !r.NeedsApproval(editCall("main.go", false, EditPair{OldText: "a", NewText: "b"}, EditPair{OldText: "c", NewText: "d"})) {
		t.Fatal("a real multi-edit must ask")
	}
}

func TestEditFileMultiDecoding(t *testing.T) {
	t.Run("native edits", func(t *testing.T) {
		c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolEditFile, Arguments: `{"path":"a.go","edits":[{"old_text":"a","new_text":"b"},{"old_text":"c","new_text":""}],"dry_run":true}`}})[0]
		if c.InputErr != "" || c.EditCount != 2 || !c.DryRun || c.OldText != "a" || c.NewText != "b" {
			t.Fatalf("decoded %+v", c)
		}
	})
	t.Run("legacy single pair unchanged", func(t *testing.T) {
		c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolEditFile, Arguments: `{"path":"a.go","old_text":"a","new_text":"b"}`}})[0]
		if c.InputErr != "" || c.EditCount != 0 || c.DryRun || len(EditPairs(c)) != 1 {
			t.Fatalf("decoded %+v", c)
		}
	})
	for name, args := range map[string]string{
		"both forms":     `{"path":"a.go","old_text":"a","new_text":"b","edits":[{"old_text":"c","new_text":"d"}]}`,
		"empty old_text": `{"path":"a.go","edits":[{"old_text":"a","new_text":"b"},{"old_text":"","new_text":"d"}]}`,
		"identical pair": `{"path":"a.go","edits":[{"old_text":"a","new_text":"a"}]}`,
		"neither form":   `{"path":"a.go"}`,
		"too many edits": `{"path":"a.go","edits":[` + strings.TrimSuffix(strings.Repeat(`{"old_text":"a","new_text":"b"},`, MaxEditFileEdits+1), ",") + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolEditFile, Arguments: args}})[0]; c.InputErr == "" {
				t.Fatalf("expected an input error for %s", args)
			}
		})
	}
	t.Run("fenced", func(t *testing.T) {
		c := Parse("```tool edit_file a.go\n{\"edits\":[{\"old_text\":\"a\",\"new_text\":\"b\"},{\"old_text\":\"c\",\"new_text\":\"d\"}],\"dry_run\":true}\n```\n")[0]
		if c.InputErr != "" || c.EditCount != 2 || !c.DryRun {
			t.Fatalf("decoded %+v", c)
		}
	})
}

func TestEditIdentityCoversEveryEditAndDryRun(t *testing.T) {
	base := editCall("a.go", false, EditPair{OldText: "a", NewText: "b"}, EditPair{OldText: "c", NewText: "d"})
	variants := []Call{
		editCall("a.go", false, EditPair{OldText: "a", NewText: "b"}),
		editCall("a.go", false, EditPair{OldText: "a", NewText: "b"}, EditPair{OldText: "c", NewText: "e"}),
		editCall("a.go", true, EditPair{OldText: "a", NewText: "b"}, EditPair{OldText: "c", NewText: "d"}),
		editCall("a.go", false, EditPair{OldText: "a\x00b", NewText: ""}, EditPair{OldText: "c", NewText: "d"}),
	}
	for i, v := range variants {
		if EditIdentity(v) == EditIdentity(base) {
			t.Errorf("variant %d collided with the base identity", i)
		}
	}
	legacy := Call{Tool: ToolEditFile, Path: "a.go", OldText: "a", NewText: "b"}
	if EditIdentity(legacy) != EditIdentity(variants[0]) {
		t.Error("a one-element edits list and the legacy pair should share an identity")
	}
}

func TestRenderUnifiedDiffHunks(t *testing.T) {
	old := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16"}, "\n") + "\n"
	updated := strings.Replace(strings.Replace(old, "2\n", "two\n", 1), "15\n", "fifteen\n", 1)
	diff := RenderUnifiedDiff("n.txt", old, updated)
	if strings.Count(diff, "@@ ") != 2 {
		t.Fatalf("expected two separate hunks:\n%s", diff)
	}
	for _, want := range []string{"@@ -1,5 +1,5 @@", "-2\n+two", "@@ -12,5 +12,5 @@", "-15\n+fifteen"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	merged := RenderUnifiedDiff("n.txt", old, strings.Replace(strings.Replace(old, "2\n", "two\n", 1), "6\n", "six\n", 1))
	if strings.Count(merged, "@@ ") != 1 {
		t.Fatalf("nearby changes should share one hunk:\n%s", merged)
	}
}
