package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseSingleWrite(t *testing.T) {
	reply := "Sure, here is the script:\n\n" +
		"```tool write_file scripts/hello.sh\n" +
		"#!/bin/sh\necho hello\n" +
		"```\n\nDone."
	calls := Parse(reply)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Tool != ToolWriteFile || c.Path != "scripts/hello.sh" {
		t.Errorf("call = %+v", c)
	}
	if c.Body != "#!/bin/sh\necho hello\n" {
		t.Errorf("body = %q", c.Body)
	}
}

func TestParseMultipleAndPathless(t *testing.T) {
	reply := "```tool list_dir\n```\nthen\n```tool read_file a.txt\n```"
	calls := Parse(reply)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(calls), calls)
	}
	if calls[0].Tool != ToolListDir || calls[0].Path != "" {
		t.Errorf("first = %+v", calls[0])
	}
	if calls[1].Tool != ToolReadFile || calls[1].Path != "a.txt" {
		t.Errorf("second = %+v", calls[1])
	}
}

func TestParseLongFenceWrapsInnerCode(t *testing.T) {
	reply := "````tool write_file README.md\n" +
		"example:\n```go\nfmt.Println(1)\n```\n" +
		"````"
	calls := Parse(reply)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].Body, "```go") {
		t.Errorf("inner fence lost: %q", calls[0].Body)
	}
}

func TestParseIgnoresPlainCodeAndUnterminated(t *testing.T) {
	if got := Parse("```go\nfmt.Println(1)\n```"); len(got) != 0 {
		t.Errorf("plain code parsed as tool: %+v", got)
	}
	if got := Parse("```tool write_file x\nno closing fence"); len(got) != 0 {
		t.Errorf("unterminated block parsed: %+v", got)
	}
	if got := Parse("no tools here"); len(got) != 0 {
		t.Errorf("prose parsed: %+v", got)
	}
}

func TestRunnerWriteReadList(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)

	res := r.Execute(Call{Tool: ToolWriteFile, Path: "scripts/hello.sh", Body: "#!/bin/sh\necho hi\n"})
	if res.Err != nil {
		t.Fatalf("write: %v", res.Err)
	}
	if !strings.Contains(res.Output, "scripts/hello.sh") {
		t.Errorf("write output = %q", res.Output)
	}

	res = r.Execute(Call{Tool: ToolReadFile, Path: "scripts/hello.sh"})
	if res.Err != nil || res.Output != "#!/bin/sh\necho hi\n" {
		t.Errorf("read = %q err=%v", res.Output, res.Err)
	}

	res = r.Execute(Call{Tool: ToolListDir})
	if res.Err != nil || res.Output != "scripts/" {
		t.Errorf("list root = %q err=%v", res.Output, res.Err)
	}
	res = r.Execute(Call{Tool: ToolListDir, Path: "scripts"})
	if res.Err != nil || res.Output != "hello.sh" {
		t.Errorf("list scripts = %q err=%v", res.Output, res.Err)
	}
}

func TestRunnerConfinement(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, path := range []string{"../escape.txt", "/etc/passwd", "a/../../b"} {
		if res := r.Execute(Call{Tool: ToolReadFile, Path: path}); res.Err == nil {
			t.Errorf("read %q: expected confinement error", path)
		}
		if res := r.Execute(Call{Tool: ToolWriteFile, Path: path, Body: "x"}); res.Err == nil {
			t.Errorf("write %q: expected confinement error", path)
		}
	}
}

func TestRunnerSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test skipped on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	if res := r.Execute(Call{Tool: ToolReadFile, Path: "link.txt"}); res.Err == nil {
		t.Errorf("symlink escape allowed: %q", res.Output)
	}
}

// TestRunnerSymlinkEscapeNewFile covers write_file targeting a path that
// does not exist yet inside a symlinked directory. EvalSymlinks requires the
// final component to exist, so checking only the full target path (rather
// than walking up to the deepest existing ancestor) would let this through.
func TestRunnerSymlinkEscapeNewFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test skipped on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	res := r.Execute(Call{Tool: ToolWriteFile, Path: "evil/newfile.txt", Body: "leaked"})
	if res.Err == nil {
		t.Fatalf("symlink escape via new file allowed: %q", res.Output)
	}
	if _, err := os.Stat(filepath.Join(outside, "newfile.txt")); err == nil {
		t.Fatal("file was written outside the workspace")
	}
}

func TestRunnerLimitsAndErrors(t *testing.T) {
	r := NewRunner(t.TempDir(), 1) // 1 KB cap

	big := strings.Repeat("a", 2048)
	if res := r.Execute(Call{Tool: ToolWriteFile, Path: "big.txt", Body: big}); res.Err == nil {
		t.Error("oversized write allowed")
	}

	if res := r.Execute(Call{Tool: ToolReadFile, Path: "missing.txt"}); res.Err == nil {
		t.Error("missing file read succeeded")
	}
	if res := r.Execute(Call{Tool: ToolWriteFile}); res.Err == nil {
		t.Error("pathless write succeeded")
	}
	if res := r.Execute(Call{Tool: "delete_everything", Path: "x"}); res.Err == nil {
		t.Error("unknown tool succeeded")
	}
}

func TestRunnerReadTruncation(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 1)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("b", 3000)), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.Execute(Call{Tool: ToolReadFile, Path: "big.txt"})
	if res.Err != nil {
		t.Fatalf("read: %v", res.Err)
	}
	if !strings.Contains(res.Output, "truncated") || len(res.Output) > 1200 {
		t.Errorf("expected truncated output, got %d bytes", len(res.Output))
	}
}

func TestRunCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	root := t.TempDir()
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolRunCommand, Body: "echo hello\n"})
	if res.Err != nil || res.Output != "hello" {
		t.Errorf("echo = %q err=%v", res.Output, res.Err)
	}

	// Runs in the workspace directory.
	res = r.Execute(Call{Tool: ToolRunCommand, Body: "pwd"})
	if res.Err != nil {
		t.Fatalf("pwd: %v", res.Err)
	}
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(res.Output)
	if got != want {
		t.Errorf("pwd = %q, want %q", got, want)
	}

	// Failures surface the exit error and keep the output.
	res = r.Execute(Call{Tool: ToolRunCommand, Body: "sh -c 'echo oops >&2; exit 3'"})
	if res.Err == nil || !strings.Contains(res.Output, "oops") {
		t.Errorf("failed command: out=%q err=%v", res.Output, res.Err)
	}

	if res := r.Execute(Call{Tool: ToolRunCommand, Body: ""}); res.Err == nil {
		t.Error("empty command allowed")
	}
	if res := r.Execute(Call{Tool: ToolRunCommand, Body: "echo a\necho b\n"}); res.Err == nil {
		t.Error("multi-line command allowed")
	}
}

func TestRunCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	r := NewRunner(t.TempDir(), 64)
	r.CommandTimeout = 100 * time.Millisecond
	res := r.Execute(Call{Tool: ToolRunCommand, Body: "sleep 5"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Errorf("timeout not enforced: %v", res.Err)
	}
}

func TestSanitizedEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/home/x", "LANG=C",
		"LLMTUI_API_KEY=hunter2", "OPENAI_API_KEY=sk-x", "MY_SECRET=x",
		"DB_PASSWORD=x", "AUTH_TOKEN=x", "AWS_CREDENTIALS=x", "LLMTUI_MODEL=qwen3",
	}
	out := strings.Join(sanitizedEnv(in), "\n")
	for _, keep := range []string{"PATH=", "HOME=", "LANG="} {
		if !strings.Contains(out, keep) {
			t.Errorf("sanitizedEnv dropped %s", keep)
		}
	}
	for _, drop := range []string{"KEY", "SECRET", "PASSWORD", "TOKEN", "CREDENTIALS", "LLMTUI_"} {
		if strings.Contains(out, drop) {
			t.Errorf("sanitizedEnv leaked a var containing %s:\n%s", drop, out)
		}
	}
}

func TestWriteFileBlocksGitInternals(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, path := range []string{".git/hooks/pre-commit", "sub/.git/config", ".git/HEAD"} {
		if res := r.Execute(Call{Tool: ToolWriteFile, Path: path, Body: "evil"}); res.Err == nil {
			t.Errorf("write into %q allowed", path)
		}
	}
	// A file merely named like git things is fine.
	if res := r.Execute(Call{Tool: ToolWriteFile, Path: "gitnotes.md", Body: "ok"}); res.Err != nil {
		t.Errorf("gitnotes.md rejected: %v", res.Err)
	}
}

func TestNeedsApproval(t *testing.T) {
	cases := []struct {
		call Call
		want bool
	}{
		{Call{Tool: ToolListDir}, false},
		{Call{Tool: ToolReadFile, Path: "a.txt"}, false},
		{Call{Tool: ToolWriteFile, Path: "a.txt", Body: "x"}, true},
		{Call{Tool: ToolRunCommand, Body: "ls -la"}, false},
		{Call{Tool: ToolRunCommand, Body: "rm -rf ."}, true},
		{Call{Tool: "mystery"}, true},
	}
	for _, c := range cases {
		if got := NeedsApproval(c.call); got != c.want {
			t.Errorf("NeedsApproval(%+v) = %v, want %v", c.call, got, c.want)
		}
	}
}

func TestFormatResults(t *testing.T) {
	out := FormatResults([]Result{
		{Call: Call{Tool: ToolWriteFile, Path: "a.sh"}, Output: "wrote 10 bytes to a.sh"},
		{Call: Call{Tool: ToolReadFile, Path: "missing"}, Err: os.ErrNotExist},
	})
	if !strings.HasPrefix(out, ResultsPrefix) {
		t.Errorf("missing prefix: %q", out)
	}
	for _, want := range []string{"### write_file a.sh", "wrote 10 bytes", "### read_file missing", "error:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestInstructionsMentionEveryTool(t *testing.T) {
	ins := Instructions("/tmp/project", false)
	for _, want := range []string{ToolListDir, ToolReadFile, ToolWriteFile, ResultsPrefix, "/tmp/project"} {
		if !strings.Contains(ins, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
}

func TestDescribeMCPCall(t *testing.T) {
	c := Call{MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{"issue_key":"AIPO-82"}`}
	got := c.Describe()
	want := `jiraWorklog: session_start({"issue_key":"AIPO-82"})`
	if got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
}
