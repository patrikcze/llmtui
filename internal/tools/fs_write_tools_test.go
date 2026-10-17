package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestCreateDirectory(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "file.txt", "x")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolCreateDirectory, Path: "a/b/c"})
	if res.Err != nil || res.Meta.Effect != EffectChanged {
		t.Fatalf("create: %v %+v", res.Err, res.Meta)
	}
	if info, err := os.Stat(filepath.Join(root, "a/b/c")); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	again := r.Execute(Call{Tool: ToolCreateDirectory, Path: "a/b/c"})
	if again.Err != nil || again.Meta.Effect != EffectUnchanged {
		t.Fatalf("idempotent create: %v %+v", again.Err, again.Meta)
	}
	if res := r.Execute(Call{Tool: ToolCreateDirectory, Path: "file.txt"}); res.Err == nil {
		t.Fatal("creating over an existing file must fail")
	}
}

// Workspace Tool Safety Invariant: create_directory obeys the write
// guardrails and lexical confinement.
func TestCreateDirectoryGuardrails(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, p := range []string{"../outside", ".git/hooks/x", ".ssh/keys", ".llmtui/skills/x", ".", "/tmp/abs"} {
		if res := r.Execute(Call{Tool: ToolCreateDirectory, Path: p}); res.Err == nil {
			t.Errorf("create_directory %q was not refused", p)
		}
	}
}

func TestMoveFileRenamesFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.txt", "alpha")
	writeSearchFixture(t, root, "dir/inner.txt", "inner")
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolMoveFile, Path: "a.txt", Destination: "nested/new/b.txt"})
	if res.Err != nil || res.Meta.Effect != EffectChanged {
		t.Fatalf("move file: %v %+v", res.Err, res.Meta)
	}
	if got := readFixture(t, root, "nested/new/b.txt"); got != "alpha" {
		t.Fatalf("moved content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still present: %v", err)
	}
	if res := r.Execute(Call{Tool: ToolMoveFile, Path: "dir", Destination: "renamed"}); res.Err != nil {
		t.Fatalf("move dir: %v", res.Err)
	}
	if got := readFixture(t, root, "renamed/inner.txt"); got != "inner" {
		t.Fatalf("moved dir content = %q", got)
	}
}

// move_file never overwrites: an existing destination is refused and both
// files keep their bytes.
func TestMoveFileNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "src.txt", "source")
	writeSearchFixture(t, root, "dst.txt", "precious")
	writeSearchFixture(t, root, "dstdir/keep.txt", "keep")
	r := NewRunner(root, 64)
	for _, dst := range []string{"dst.txt", "dstdir"} {
		res := r.Execute(Call{Tool: ToolMoveFile, Path: "src.txt", Destination: dst})
		if res.Err == nil || res.Meta.Error == nil || res.Meta.Error.Code != "destination_exists" {
			t.Fatalf("move onto %s: err=%v meta=%+v", dst, res.Err, res.Meta.Error)
		}
	}
	if readFixture(t, root, "src.txt") != "source" || readFixture(t, root, "dst.txt") != "precious" || readFixture(t, root, "dstdir/keep.txt") != "keep" {
		t.Fatal("a refused move changed file content")
	}
}

// The atomic no-replace rename must refuse a destination that appears after
// the admission checks (an external writer racing the move).
func TestMoveFileRefusesDestinationCreatedAfterChecks(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "src.txt", "source")
	moveBeforeRename = func() { writeSearchFixture(t, root, "late.txt", "external") }
	t.Cleanup(func() { moveBeforeRename = nil })

	res := NewRunner(root, 64).Execute(Call{Tool: ToolMoveFile, Path: "src.txt", Destination: "late.txt"})
	if res.Err == nil {
		t.Fatal("move replaced a destination created after the checks")
	}
	if readFixture(t, root, "late.txt") != "external" || readFixture(t, root, "src.txt") != "source" {
		t.Fatal("the racing destination or the source was changed")
	}
}

// Workspace Tool Safety Invariants: both ends of a move go through the write
// guardrails and confinement, and secret classification on the logical path
// cannot be laundered by a rename.
func TestMoveFileGuardrails(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.txt", "a")
	writeSearchFixture(t, root, ".env", "TOKEN=1")
	writeSearchFixture(t, root, "certs/server.pem", "pem")
	writeSearchFixture(t, root, ".git/config", "cfg")
	writeSearchFixture(t, root, "dir/x.txt", "x")
	r := NewRunner(root, 64)
	tests := []struct{ name, src, dst string }{
		{"destination outside", "a.txt", "../a.txt"},
		{"destination absolute", "a.txt", "/tmp/a.txt"},
		{"source outside", "../a.txt", "b.txt"},
		{"into .git", "a.txt", ".git/hooks/pre-commit"},
		{"out of .git", ".git/config", "config"},
		{"into .ssh", "a.txt", ".ssh/authorized_keys"},
		{"into .llmtui", "a.txt", ".llmtui/skills/x.md"},
		{"shell startup", "a.txt", ".bashrc"},
		{"secret to plain name", ".env", "notes.txt"},
		{"normalized secret to plain name", "x/../.env", "env-backup.txt"},
		{"key to plain name", "certs/server.pem", "certs/server.txt"},
		{"directory into itself", "dir", "dir/sub"},
		{"root", ".", "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if res := r.Execute(Call{Tool: ToolMoveFile, Path: tc.src, Destination: tc.dst}); res.Err == nil {
				t.Fatalf("move %s -> %s was not refused", tc.src, tc.dst)
			}
		})
	}
	if readFixture(t, root, ".env") != "TOKEN=1" || readFixture(t, root, "a.txt") != "a" {
		t.Fatal("a refused move changed files")
	}
	// A secret staying secret-named is an ordinary move.
	if res := r.Execute(Call{Tool: ToolMoveFile, Path: ".env", Destination: "config/.env"}); res.Err != nil {
		t.Fatalf("secret to secret-named move: %v", res.Err)
	}
}

func TestMoveFileDecoding(t *testing.T) {
	c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolMoveFile, Arguments: `{"source":"a.txt","destination":"b.txt"}`}})[0]
	if c.InputErr != "" || c.Path != "a.txt" || c.Destination != "b.txt" {
		t.Fatalf("native decode: %+v", c)
	}
	for _, args := range []string{`{"source":"a.txt"}`, `{"source":"a","destination":"./a"}`, `{"path":"a","destination":"b"}`} {
		if c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolMoveFile, Arguments: args}})[0]; c.InputErr == "" {
			t.Errorf("expected input error for %s", args)
		}
	}
	f := Parse("```tool move_file\n{\"source\":\"a.txt\",\"destination\":\"b.txt\"}\n```\n")[0]
	if f.InputErr != "" || f.Path != "a.txt" || f.Destination != "b.txt" {
		t.Fatalf("fenced decode: %+v", f)
	}
	if f := Parse("```tool move_file a.txt\n{\"destination\":\"b.txt\"}\n```\n")[0]; f.InputErr != "" || f.Path != "a.txt" {
		t.Fatalf("fenced source in fence: %+v", f)
	}
	if !strings.Contains(c.Describe(), "a.txt → b.txt") {
		t.Fatalf("describe = %q", c.Describe())
	}
}

func TestStructuralWriteToolsAreExclusiveAndAsk(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, c := range []Call{{Tool: ToolCreateDirectory, Path: "x"}, {Tool: ToolMoveFile, Path: "a", Destination: "b"}} {
		if ConcurrentSafe(c) {
			t.Errorf("%s must not run concurrently", c.Tool)
		}
		if !r.NeedsApproval(c) {
			t.Errorf("%s must ask", c.Tool)
		}
	}
}

// TestParseLiftsJSONWrittenOnTheFenceLine: Gemma 4 E4B wrote a fenced
// move_file with its JSON object on the fence line and an empty body, the
// form the instructions document for web_fetch. It was rejected as missing a
// body. A JSON object on the fence line with no body is now the body; an
// explicit body still wins, and an ordinary path is untouched.
func TestParseLiftsJSONWrittenOnTheFenceLine(t *testing.T) {
	fence := "```"
	t.Run("move_file", func(t *testing.T) {
		calls := Parse(fence + `tool move_file {"source":"docs/README.md","destination":"docs/math/README.md"}` + "\n" + fence)
		if len(calls) != 1 || calls[0].InputErr != "" {
			t.Fatalf("calls = %+v, want one valid move_file", calls)
		}
		if calls[0].Path != "docs/README.md" || calls[0].Destination != "docs/math/README.md" {
			t.Fatalf("move_file = %q -> %q", calls[0].Path, calls[0].Destination)
		}
	})
	t.Run("web_fetch documented form", func(t *testing.T) {
		calls := Parse(fence + `tool web_fetch {"url":"https://example.com/","cache_mode":"refresh"}` + "\n" + fence)
		if len(calls) != 1 || calls[0].InputErr != "" || calls[0].Path != "https://example.com/" || calls[0].WebCacheMode != "refresh" {
			t.Fatalf("calls = %+v, want web_fetch of https://example.com/ with refresh", calls)
		}
	})
	t.Run("explicit body wins", func(t *testing.T) {
		calls := Parse(fence + "tool write_file {\"x\":1}\nhello\n" + fence)
		if len(calls) != 1 || calls[0].Path != `{"x":1}` || calls[0].Body != "hello\n" {
			t.Fatalf("calls = %+v, want the fence argument kept as the path", calls)
		}
	})
	t.Run("path untouched", func(t *testing.T) {
		calls := Parse(fence + "tool read_file src/{a}.go\n" + fence)
		if len(calls) != 1 || calls[0].Path != "src/{a}.go" {
			t.Fatalf("calls = %+v, want the path kept", calls)
		}
	})
}

// TestGrepDescriptionShowsFileFilter: a grep limited by its glob filter must
// say so. Seen live: the transcript showed `grep "TODO" in .`, the model
// reported no TODO "in the project's .go files", and the README's TODO
// looked like a grep miss.
func TestGrepDescriptionShowsFileFilter(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolGrep, Arguments: `{"pattern":"TODO","glob":"*.go"}`}})
	if got := calls[0].Describe(); !strings.Contains(got, "*.go") {
		t.Fatalf("Describe() = %q, want the *.go filter shown", got)
	}
}
