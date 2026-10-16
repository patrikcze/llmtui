//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Workspace Tool Safety Invariant: confinement is enforced on the resolved,
// symlink-evaluated path. A symlink that points outside the workspace must
// not let get_file_info, directory_tree, or read_multiple_files observe it.
func TestReadOnlyFSToolsRefuseEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSearchFixture(t, outside, "secret/data.txt", "outside-data\n")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	r := NewRunner(root, 64)

	if res := r.Execute(Call{Tool: ToolGetFileInfo, Path: "linkdir/data.txt"}); res.Err == nil {
		t.Errorf("get_file_info followed an escaping symlink: %q", res.Output)
	}
	if res := r.Execute(Call{Tool: ToolDirectoryTree, Path: "linkdir"}); res.Err == nil {
		t.Errorf("directory_tree followed an escaping symlink: %q", res.Output)
	}
	res := r.Execute(multiReadCall("linkdir/data.txt"))
	if res.Err == nil || strings.Contains(res.Output+res.Err.Error(), "outside-data") {
		t.Errorf("read_multiple_files read through an escaping symlink: %v %q", res.Err, res.Output)
	}

	// The tree of the workspace itself lists the link without descending.
	tree := r.Execute(Call{Tool: ToolDirectoryTree})
	if tree.Err != nil {
		t.Fatalf("directory_tree: %v", tree.Err)
	}
	node := decodeTree(t, tree.Output)
	link := findChild(node, "linkdir")
	if link == nil || link.Type != "symlink" || link.Children != nil {
		t.Fatalf("symlink entry wrong: %s", tree.Output)
	}
	if strings.Contains(tree.Output, "data.txt") {
		t.Fatalf("directory_tree descended into a symlink: %s", tree.Output)
	}
}

func TestGetFileInfoDescribesSymlinkItself(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "real.txt", "x")
	if err := os.Symlink("real.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	res := NewRunner(root, 64).Execute(Call{Tool: ToolGetFileInfo, Path: "alias.txt"})
	if res.Err != nil || !strings.Contains(res.Output, "type: symlink") {
		t.Fatalf("get_file_info: %v %q", res.Err, res.Output)
	}
}
