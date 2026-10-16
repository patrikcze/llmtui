package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func multiReadCall(paths ...string) Call {
	c := Call{Tool: ToolReadMultipleFiles, PathCount: len(paths)}
	copy(c.Paths[:], paths)
	return c
}

func TestReadMultipleFilesReportsFailuresInline(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.txt", "alpha\n")
	writeSearchFixture(t, root, "dir/b.txt", "beta\n")
	r := NewRunner(root, 64)

	res := r.Execute(multiReadCall("a.txt", "missing.txt", "../escape.txt", "dir/b.txt"))
	if res.Err != nil {
		t.Fatalf("read_multiple_files: %v", res.Err)
	}
	for _, want := range []string{"==> a.txt <==", "alpha", "==> missing.txt <==", "[error:", "==> ../escape.txt <==", "outside the workspace", "==> dir/b.txt <==", "beta"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
	if res.Meta.Outcome != OutcomePartial || res.Meta.Effect != EffectNone {
		t.Errorf("meta = %+v, want partial outcome with no effect", res.Meta)
	}
}

func TestReadMultipleFilesAllFailingIsAnError(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	if res := r.Execute(multiReadCall("nope.txt")); res.Err == nil {
		t.Fatal("expected an error when no file could be read")
	}
}

func TestReadMultipleFilesBoundsCombinedOutput(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "big.txt", strings.Repeat("x", 900)+"\n")
	writeSearchFixture(t, root, "small.txt", "tail\n")
	r := NewRunner(root, 1) // 1 KB cap
	res := r.Execute(multiReadCall("big.txt", "big.txt", "small.txt"))
	if res.Err != nil {
		t.Fatalf("read_multiple_files: %v", res.Err)
	}
	if len(res.Output) > 2048 {
		t.Fatalf("combined output %d bytes exceeds the bound", len(res.Output))
	}
	if !strings.Contains(res.Output, "combined output limit") {
		t.Fatalf("expected a limit note:\n%s", res.Output)
	}
}

// Workspace Tool Safety Invariant: secret classification works on the
// logical path of every entry, so one secret path anywhere in the batch
// makes the whole call ask.
func TestReadMultipleFilesSecretPathNeedsApproval(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	tests := []struct {
		name  string
		paths []string
		want  bool
	}{
		{"plain files", []string{"a.go", "README.md"}, false},
		{"env file last", []string{"a.go", ".env"}, true},
		{"normalized ssh dir", []string{"x/../.ssh/config"}, true},
		{"key file", []string{"certs/server.pem"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.NeedsApproval(multiReadCall(tc.paths...)); got != tc.want {
				t.Fatalf("NeedsApproval(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

func TestReadMultipleFilesDecoding(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolReadMultipleFiles, Arguments: `{"paths":["a","b"]}`}})[0]
		if c.InputErr != "" || strings.Join(ReadPaths(c), ",") != "a,b" {
			t.Fatalf("decoded %+v", c)
		}
	})
	t.Run("native rejects too many", func(t *testing.T) {
		paths, _ := json.Marshal(make([]string, MaxReadMultipleFiles+1))
		raw := strings.ReplaceAll(string(paths), `""`, `"f"`)
		c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolReadMultipleFiles, Arguments: `{"paths":` + raw + `}`}})[0]
		if c.InputErr == "" {
			t.Fatal("expected an input error")
		}
	})
	t.Run("native rejects empty", func(t *testing.T) {
		c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolReadMultipleFiles, Arguments: `{"paths":[]}`}})[0]
		if c.InputErr == "" {
			t.Fatal("expected an input error")
		}
	})
	t.Run("fenced json", func(t *testing.T) {
		c := Parse("```tool read_multiple_files\n{\"paths\":[\"a\",\"b\"]}\n```\n")[0]
		if c.InputErr != "" || strings.Join(ReadPaths(c), ",") != "a,b" {
			t.Fatalf("decoded %+v", c)
		}
	})
	t.Run("fenced lines", func(t *testing.T) {
		c := Parse("```tool read_multiple_files a\nb\n\nc\n```\n")[0]
		if c.InputErr != "" || strings.Join(ReadPaths(c), ",") != "a,b,c" || c.Path != "" {
			t.Fatalf("decoded %+v", c)
		}
	})
}

func TestGetFileInfo(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "dir/a.txt", "hello\n")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolGetFileInfo, Path: "dir/a.txt"})
	if res.Err != nil {
		t.Fatalf("get_file_info: %v", res.Err)
	}
	// The permission bits come from the platform: Windows has no Unix modes
	// and Go synthesizes them from the read-only attribute (0666 / 0444), so
	// the expectation is derived from the fixture's own Lstat.
	fixture, err := os.Lstat(filepath.Join(root, "dir", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	perm := fixture.Mode().Perm()
	wantPerm := fmt.Sprintf("permissions: %s (%04o)", perm, perm)
	for _, want := range []string{"path: dir/a.txt", "type: file", "size: 6 bytes", wantPerm, "modified: "} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
	if strings.Contains(res.Output, "hello") {
		t.Fatal("get_file_info must not return content")
	}
	dir := r.Execute(Call{Tool: ToolGetFileInfo, Path: "dir"})
	if dir.Err != nil || !strings.Contains(dir.Output, "type: directory") {
		t.Fatalf("directory info: %v %q", dir.Err, dir.Output)
	}
	if res := r.Execute(Call{Tool: ToolGetFileInfo, Path: "../"}); res.Err == nil {
		t.Fatal("get_file_info escaped the workspace")
	}
	if r.NeedsApproval(Call{Tool: ToolGetFileInfo, Path: ".env"}) {
		t.Fatal("metadata of a secret path must not need approval")
	}
}

func decodeTree(t *testing.T, out string) treeNode {
	t.Helper()
	var node treeNode
	if err := json.Unmarshal([]byte(out), &node); err != nil {
		t.Fatalf("tree is not JSON: %v\n%s", err, out)
	}
	return node
}

func findChild(n treeNode, name string) *treeNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestDirectoryTree(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "src/main.go", "x")
	writeSearchFixture(t, root, "src/deep/a/b/c.go", "x")
	writeSearchFixture(t, root, "node_modules/pkg/index.js", "x")
	writeSearchFixture(t, root, ".git/HEAD", "x")
	writeSearchFixture(t, root, "README.md", "x")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolDirectoryTree, Max: 2, Filter: "node_modules"})
	if res.Err != nil {
		t.Fatalf("directory_tree: %v", res.Err)
	}
	tree := decodeTree(t, res.Output)
	if tree.Type != "directory" || findChild(tree, ".git") != nil || findChild(tree, "node_modules") != nil {
		t.Fatalf("unexpected top level: %s", res.Output)
	}
	src := findChild(tree, "src")
	if src == nil || findChild(*src, "main.go") == nil {
		t.Fatalf("src not listed: %s", res.Output)
	}
	deep := findChild(*src, "deep")
	if deep == nil || !deep.Truncated || len(deep.Children) != 0 {
		t.Fatalf("depth limit not marked: %s", res.Output)
	}
	if readme := findChild(tree, "README.md"); readme == nil || readme.Type != "file" || readme.Children != nil {
		t.Fatalf("file entry wrong: %s", res.Output)
	}
	if res.Meta.Outcome != OutcomePartial {
		t.Fatalf("outcome = %q, want partial for a depth-limited tree", res.Meta.Outcome)
	}

	sub := r.Execute(Call{Tool: ToolDirectoryTree, Path: "src/deep"})
	if sub.Err != nil || !strings.Contains(sub.Output, `"c.go"`) {
		t.Fatalf("subtree: %v %s", sub.Err, sub.Output)
	}
	if res := r.Execute(Call{Tool: ToolDirectoryTree, Path: "README.md"}); res.Err == nil {
		t.Fatal("directory_tree on a file must fail")
	}
	if res := r.Execute(Call{Tool: ToolDirectoryTree, Path: ".."}); res.Err == nil {
		t.Fatal("directory_tree escaped the workspace")
	}
}

func TestDirectoryTreeDecoding(t *testing.T) {
	c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolDirectoryTree, Arguments: `{"path":"src","depth":4,"exclude":["vendor"]}`}})[0]
	if c.InputErr != "" || c.Path != "src" || c.Max != 4 || c.Filter != "vendor" {
		t.Fatalf("native decode: %+v", c)
	}
	if c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolDirectoryTree, Arguments: `{"depth":11}`}})[0]; c.InputErr == "" {
		t.Fatal("depth above the maximum must be rejected")
	}
	f := Parse("```tool directory_tree src\n{\"depth\":2}\n```\n")[0]
	if f.InputErr != "" || f.Path != "src" || f.Max != 2 {
		t.Fatalf("fenced decode: %+v", f)
	}
	if f := Parse("```tool directory_tree\n```\n")[0]; f.InputErr != "" || f.Max != 0 {
		t.Fatalf("empty fenced body: %+v", f)
	}
}

func TestReadOnlyFSToolsAreConcurrentSafeAndRegistered(t *testing.T) {
	reg := DefaultRegistry()
	for _, name := range []string{ToolReadMultipleFiles, ToolGetFileInfo, ToolDirectoryTree} {
		if !ConcurrentSafe(Call{Tool: name}) {
			t.Errorf("%s should be concurrent-safe", name)
		}
		info, ok := reg.Get(name)
		if !ok || info.Safety != SafetyReadOnly || info.Approval == "" {
			t.Errorf("%s registry entry = %+v", name, info)
		}
	}
}
