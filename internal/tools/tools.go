// Package tools lets the assistant work with files in the directory llmtui
// was launched from. Tool calls are fenced blocks the model emits in its
// reply — no native function-calling support is required, so this works
// with any local model:
//
//	```tool write_file scripts/hello.sh
//	#!/bin/sh
//	echo hello
//	```
//
// Execution is confined to the workspace root: absolute paths and anything
// escaping the root are rejected.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ResultsPrefix marks the follow-up message that carries tool output back to
// the model. The TUI also uses it to restyle those messages in the viewport.
const ResultsPrefix = "[tool results]"

// Known tool names.
const (
	ToolListDir   = "list_dir"
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
)

// Call is one parsed tool invocation from an assistant reply.
type Call struct {
	Tool string
	Path string
	Body string
}

// Result is the outcome of executing one call.
type Result struct {
	Call   Call
	Output string
	Err    error
}

// fenceOpen matches a tool block opener: 3+ backticks, "tool", name, optional path.
var fenceOpen = regexp.MustCompile("^(`{3,})tool[ \t]+([a-z_]+)(?:[ \t]+(.+?))?[ \t]*$")

// Parse extracts tool calls from an assistant reply. A block opens with a
// fence whose info string is "tool <name> [path]" and closes at a line of at
// least as many backticks; longer fences may wrap bodies that themselves
// contain code fences.
func Parse(reply string) []Call {
	var calls []Call
	lines := strings.Split(reply, "\n")
	for i := 0; i < len(lines); i++ {
		open := fenceOpen.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if open == nil {
			continue
		}
		closing := regexp.MustCompile("^`{" + fmt.Sprint(len(open[1])) + ",}[ \t]*$")
		var body []string
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if closing.MatchString(strings.TrimRight(lines[j], "\r")) {
				calls = append(calls, Call{Tool: open[2], Path: strings.TrimSpace(open[3]), Body: joinBody(body)})
				i = j
				closed = true
				break
			}
			body = append(body, strings.TrimRight(lines[j], "\r"))
		}
		if !closed {
			break // unterminated block: ignore it and everything after
		}
	}
	return calls
}

func joinBody(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// Runner executes calls against a workspace directory.
type Runner struct {
	root  string
	maxKB int
}

// NewRunner confines execution to root; maxKB caps file reads and writes.
func NewRunner(root string, maxKB int) *Runner {
	if maxKB <= 0 {
		maxKB = 512
	}
	return &Runner{root: root, maxKB: maxKB}
}

// Root returns the workspace directory.
func (r *Runner) Root() string { return r.root }

// resolve turns a workspace-relative path into an absolute one, rejecting
// anything that would land outside the root (absolute paths, "..", and
// existing symlinks that point out of the workspace).
func (r *Runner) resolve(rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return r.root, nil
	}
	if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	abs := filepath.Join(r.root, rel)
	// A symlink inside the workspace must not smuggle access outside it.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		rootResolved, rerr := filepath.EvalSymlinks(r.root)
		if rerr == nil && resolved != rootResolved &&
			!strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q resolves outside the workspace", rel)
		}
	}
	return abs, nil
}

// Execute runs one call and never panics; errors land in Result.Err.
func (r *Runner) Execute(c Call) Result {
	res := Result{Call: c}
	switch c.Tool {
	case ToolListDir:
		res.Output, res.Err = r.listDir(c.Path)
	case ToolReadFile:
		res.Output, res.Err = r.readFile(c.Path)
	case ToolWriteFile:
		res.Output, res.Err = r.writeFile(c.Path, c.Body)
	default:
		res.Err = fmt.Errorf("unknown tool %q (available: %s, %s, %s)",
			c.Tool, ToolListDir, ToolReadFile, ToolWriteFile)
	}
	return res
}

const maxDirEntries = 200

func (r *Runner) listDir(rel string) (string, error) {
	abs, err := r.resolve(rel)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("list directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for i, e := range entries {
		if i >= maxDirEntries {
			fmt.Fprintf(&b, "… and %d more entries\n", len(entries)-maxDirEntries)
			break
		}
		if e.IsDir() {
			b.WriteString(e.Name() + "/\n")
		} else {
			b.WriteString(e.Name() + "\n")
		}
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (r *Runner) readFile(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("read_file needs a path")
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory (use list_dir)", rel)
	}
	limit := int64(r.maxKB) * 1024
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > limit {
		return string(data[:limit]) + fmt.Sprintf("\n… truncated (%d of %d bytes shown)", limit, info.Size()), nil
	}
	return string(data), nil
}

func (r *Runner) writeFile(rel, content string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("write_file needs a path")
	}
	if len(content) > r.maxKB*1024 {
		return "", fmt.Errorf("content exceeds the %d KB write limit", r.maxKB)
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return "", fmt.Errorf("%q is a directory", rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), rel), nil
}

// FormatResults renders execution results as the follow-up message body.
func FormatResults(results []Result) string {
	var b strings.Builder
	b.WriteString(ResultsPrefix + "\n")
	for _, res := range results {
		target := res.Call.Tool
		if res.Call.Path != "" {
			target += " " + res.Call.Path
		}
		fmt.Fprintf(&b, "\n### %s\n", target)
		if res.Err != nil {
			b.WriteString("error: " + res.Err.Error() + "\n")
			continue
		}
		b.WriteString(res.Output + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Instructions is appended to the system prompt while tools are enabled.
func Instructions(root string) string {
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using tools.
To use a tool, emit a fenced code block whose info string is "tool <name> [path]". Available tools:

- list_dir [path] — list a directory (path optional, defaults to the project root)
- read_file <path> — return a file's contents
- write_file <path> — create or overwrite a file with the block's body

Example — save a script:

`+"```"+`tool write_file scripts/hello.sh
#!/bin/sh
echo hello
`+"```"+`

Rules:
- Paths are always relative to the project root; never use absolute paths or "..".
- After you emit tool blocks, stop and wait: the results come back in the next user message, marked "%s".
- Use one block per action. If a body contains triple backticks, open the tool block with four.
- When the task is complete, reply normally without any tool blocks.`, root, ResultsPrefix))
}
