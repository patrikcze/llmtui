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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/procutil"
)

// ResultsPrefix marks the follow-up message that carries tool output back to
// the model. The TUI also uses it to restyle those messages in the viewport.
const ResultsPrefix = "[tool results]"

// Known tool names.
const (
	ToolListDir    = "list_dir"
	ToolReadFile   = "read_file"
	ToolWriteFile  = "write_file"
	ToolRunCommand = "run_command"
	ToolWebSearch  = "web_search"
	ToolWebFetch   = "web_fetch"
	// ToolSkillLoad activates a skill (declarative task instructions) for the
	// current agent run. It executes no code and grants no permissions: the
	// skill body is included by the prompt composer on the next inference.
	ToolSkillLoad = "skill_load"
)

// Call is one tool invocation: parsed from a fenced block in an assistant
// reply, or converted from a native function call (in which case ID is set
// and the results must go back as role:"tool" messages).
type Call struct {
	ID   string
	Tool string
	Path string
	Body string
	// InputErr records malformed native JSON arguments. The call remains in
	// the batch so the model receives a correlated tool error, but Execute
	// must not run a zero-valued approximation of the requested operation.
	InputErr string
	// Max caps web_search results (native max_results argument).
	Max int

	// MCPServer, when non-empty, marks this as a call to an MCP server's
	// tool rather than a built-in one. MCPTool is the tool's name on that
	// server, and MCPArgs is the raw JSON arguments to pass through
	// unparsed — MCP tool schemas are arbitrary and unknown to this
	// package, unlike the built-in tools' hand-mapped Path/Body/Max.
	MCPServer string
	MCPTool   string
	MCPArgs   string
}

// Result is the outcome of executing one call. Diff is a display-only
// rendering of what a write_file changed (see RenderWriteDiff); it is shown
// in the TUI but never sent to the model.
type Result struct {
	Call   Call
	Output string
	Diff   string
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
	// execution serializes calls made through this runner. A tool batch is
	// ordered, but cancellation/resend can briefly leave an old command
	// goroutine alive while a new batch starts; allowing both to mutate the
	// workspace would make their effects race.
	execution chan struct{}

	// CommandTimeout bounds run_command execution (default 30s).
	CommandTimeout time.Duration

	// Web enables web_search/web_fetch when non-nil; WebMaxResults caps
	// search hits per call.
	Web           WebClient
	WebMaxResults int

	// Guardrails governs write blocks (.git, key material, shell startup
	// files), command classification, and secret-read approval. Defaults to
	// DefaultGuardrails (everything on).
	Guardrails GuardrailPolicy

	// Skills enables the skill_load tool when non-nil (mirrors Web). The
	// implementation validates the ID and marks the skill active for the
	// current run; it must not execute anything.
	Skills SkillLoader
}

// SkillLoader activates one skill for the current agent run. Implemented by
// the TUI's skill manager adapter; the tools package stays unaware of skill
// storage and prompt composition.
type SkillLoader interface {
	// LoadSkillForRun validates and activates the skill, returning the
	// confirmation text sent back to the model as the tool result.
	LoadSkillForRun(id string) (string, error)
}

// NewRunner confines execution to root; maxKB caps file reads and writes.
func NewRunner(root string, maxKB int) *Runner {
	if maxKB <= 0 {
		maxKB = 512
	}
	return &Runner{
		root:           root,
		maxKB:          maxKB,
		execution:      make(chan struct{}, 1),
		CommandTimeout: 30 * time.Second,
		Guardrails:     DefaultGuardrails(),
	}
}

// Root returns the workspace directory.
func (r *Runner) Root() string { return r.root }

// MaxResultBytes returns the output cap this runner applies to file reads and
// command output, so other tool sources (MCP) can bound their results the
// same way.
func (r *Runner) MaxResultBytes() int { return r.maxKB * 1024 }

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
	if r.Guardrails.BlockSymlinkEscape {
		if err := r.checkSymlinkEscape(abs); err != nil {
			return "", fmt.Errorf("path %q resolves outside the workspace", rel)
		}
	}
	return abs, nil
}

// checkSymlinkEscape walks up from abs to the deepest ancestor that exists,
// resolves any symlinks in that ancestor, and rejects the path if the
// resolved ancestor falls outside the workspace root. Checking only abs
// itself (via a single EvalSymlinks call) misses the common write_file case:
// EvalSymlinks requires the final component to exist, so a not-yet-created
// file inside a symlinked directory would skip the check entirely.
func (r *Runner) checkSymlinkEscape(abs string) error {
	rootResolved, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return nil // can't resolve the root itself; nothing to compare against
	}
	dir := abs
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
				return fmt.Errorf("resolves outside the workspace")
			}
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // reached filesystem root without finding an existing ancestor
		}
		dir = parent
	}
}

// Execute runs one call and never panics; errors land in Result.Err.
func (r *Runner) Execute(c Call) Result {
	return r.ExecuteContext(context.Background(), c)
}

// ExecuteContext runs one serialized call. Cancellation is honored while a
// call waits for the runner and is propagated to commands and web requests.
func (r *Runner) ExecuteContext(ctx context.Context, c Call) Result {
	res := Result{Call: c}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case r.execution <- struct{}{}:
		defer func() { <-r.execution }()
	case <-ctx.Done():
		res.Err = fmt.Errorf("tool call cancelled: %w", ctx.Err())
		return res
	}
	if err := ctx.Err(); err != nil {
		res.Err = fmt.Errorf("tool call cancelled: %w", err)
		return res
	}
	if c.InputErr != "" {
		res.Err = fmt.Errorf("invalid arguments for %s: %s", c.Tool, c.InputErr)
		return res
	}
	switch c.Tool {
	case ToolListDir:
		res.Output, res.Err = r.listDir(c.Path)
	case ToolReadFile:
		res.Output, res.Err = r.readFile(c.Path)
	case ToolWriteFile:
		res.Output, res.Diff, res.Err = r.writeFile(c.Path, c.Body)
	case ToolRunCommand:
		res.Output, res.Err = r.runCommandContext(ctx, c.Body)
	case ToolWebSearch:
		res.Output, res.Err = r.webSearch(ctx, c)
	case ToolWebFetch:
		res.Output, res.Err = r.webFetch(ctx, c)
	case ToolSkillLoad:
		res.Output, res.Err = r.skillLoad(c)
	default:
		res.Err = fmt.Errorf("%w %q (built-in: %s, %s, %s, %s, %s, %s)",
			ErrUnknownTool, c.Tool, ToolListDir, ToolReadFile, ToolWriteFile, ToolRunCommand, ToolWebSearch, ToolWebFetch)
	}
	return res
}

// ErrUnknownTool marks a call whose tool name matched nothing. Callers that
// know about additional tools (the TUI's MCP integration) detect it with
// errors.Is and append their own tool names, so the model is never told the
// built-ins are the complete set when they aren't — a model that mangles an
// MCP name (e.g. "mcp_srv_tool" for "mcp__srv__tool") must see the correct
// names to self-correct instead of concluding the tools don't exist.
var ErrUnknownTool = errors.New("unknown tool")

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

func (r *Runner) writeFile(rel, content string) (output, diff string, err error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", fmt.Errorf("write_file needs a path")
	}
	rel = filepath.Clean(rel)
	// Block writes into .git (a hook would execute on the next git command),
	// key-material directories, and shell startup files.
	if msg := r.Guardrails.checkWritePath(rel); msg != "" {
		return "", "", errors.New(msg)
	}
	if len(content) > r.maxKB*1024 {
		return "", "", fmt.Errorf("content exceeds the %d KB write limit", r.maxKB)
	}
	abs, err := r.resolve(rel)
	if err != nil {
		return "", "", err
	}
	// Capture the previous content so the TUI can show what changed.
	existed := false
	oldContent := ""
	oldTooBig := false
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return "", "", fmt.Errorf("%q is a directory", rel)
		}
		existed = true
		if info.Size() <= int64(r.maxKB)*1024 {
			if data, rerr := os.ReadFile(abs); rerr == nil {
				oldContent = string(data)
			} else {
				oldTooBig = true // unreadable: treat like undiffable
			}
		} else {
			oldTooBig = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", "", fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", "", fmt.Errorf("write file: %w", err)
	}
	if oldTooBig {
		diff = fmt.Sprintf("Update(%s) — previous content replaced (too large to diff)", rel)
	} else {
		diff = RenderWriteDiff(rel, oldContent, content, existed)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), rel), diff, nil
}

// runCommand executes one shell command in the workspace directory. The
// shell is picked per OS (sh on Unix, cmd on Windows), output is size-capped,
// execution is time-limited, and the environment is sanitized so secrets in
// the parent process never reach the command (or, through its output, the
// model).
func (r *Runner) runCommand(body string) (string, error) {
	return r.runCommandContext(context.Background(), body)
}

func (r *Runner) runCommandContext(parent context.Context, body string) (string, error) {
	cmdline := strings.TrimSpace(body)
	if cmdline == "" {
		return "", fmt.Errorf("run_command needs a command in the block body")
	}
	if strings.ContainsAny(cmdline, "\n\r") {
		return "", fmt.Errorf("one command per block — multi-line scripts must be saved with write_file first")
	}

	timeout := r.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	cmd.Dir = r.root
	cmd.Env = sanitizedEnv(os.Environ())
	procutil.SetupProcAttr(cmd)
	// A descendant retaining stdout/stderr must not keep CombinedOutput
	// blocked indefinitely after the context kills the direct shell.
	cmd.WaitDelay = time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}
	if err := procutil.TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("contain command process tree: %w", err)
	}
	err := cmd.Wait()
	// Commands are synchronous by contract (one command per block), so any
	// process still in the group — a backgrounded `cmd &`, a timed-out tree —
	// must not outlive the tool call.
	procutil.KillGroup(cmd)
	output := strings.TrimRight(out.String(), "\n")
	if limit := r.maxKB * 1024; len(output) > limit {
		output = output[:limit] + "\n… output truncated"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return output, fmt.Errorf("command timed out after %s", timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return output, fmt.Errorf("command cancelled: %w", ctx.Err())
	}
	if err != nil {
		return output, fmt.Errorf("command failed: %w", err)
	}
	if output == "" {
		output = "(no output)"
	}
	return output, nil
}

// skillLoad activates a skill for the current run via the configured
// SkillLoader. It is deliberately side-effect free beyond prompt state:
// unknown IDs and validation failures come back as recoverable tool errors
// the model can correct from.
func (r *Runner) skillLoad(c Call) (string, error) {
	id := strings.TrimSpace(c.Path)
	if id == "" {
		id = strings.TrimSpace(c.Body)
	}
	if id == "" {
		return "", fmt.Errorf("skill_load needs a skill id")
	}
	if r.Skills == nil {
		return "", fmt.Errorf("skills are not available in this session")
	}
	return r.Skills.LoadSkillForRun(id)
}

// secretEnvPattern matches environment variable names that likely hold
// credentials; those never reach commands the model runs.
var secretEnvPattern = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential|passphrase|(^|_)pass(_|$)|(^|_)(url|dsn)(_|$)|conn(ection)?_?string)`)

var sensitiveEnvNames = map[string]bool{
	"SSH_AUTH_SOCK": true,
	"KUBECONFIG":    true,
	"VAULT_ADDR":    true,
}

func sanitizedEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "LLMTUI_") || sensitiveEnvNames[strings.ToUpper(name)] || secretEnvPattern.MatchString(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// NeedsApproval reports whether a call mutates state or runs code and must
// be confirmed by the user (unless approvals are set to auto). Read-only
// calls and provably read-only commands run without asking. This is the
// policy-free view; a Runner applies its GuardrailPolicy (secret-read
// approval) via its own NeedsApproval method.
func NeedsApproval(c Call) bool {
	switch c.Tool {
	case ToolListDir, ToolReadFile, ToolWebSearch, ToolSkillLoad:
		return false
	case ToolRunCommand:
		return ClassifyCommand(c.Body).Verdict != VerdictAuto
	default:
		return true
	}
}

// NeedsApproval reports whether a call must be confirmed under this runner's
// guardrail policy. It matches the package-level NeedsApproval but adds
// secret-read gating: read_file of a likely secret file (.env, *.pem,
// id_rsa, …) asks first when RequireApprovalForSecretReads is on.
func (r *Runner) NeedsApproval(c Call) bool {
	switch c.Tool {
	case ToolListDir, ToolWebSearch, ToolSkillLoad:
		return false
	case ToolReadFile:
		return r.Guardrails.RequireApprovalForSecretReads && IsSecretPath(c.Path)
	case ToolRunCommand:
		return r.Guardrails.ClassifyCommand(c.Body, r.root).Verdict != VerdictAuto
	default:
		return true
	}
}

// autoAllowedCommands are read-only inspection commands that may run without
// per-call approval, provided the command line has no shell metacharacters.
var autoAllowedCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true,
	"rg": true, "find": true, "wc": true, "pwd": true, "file": true,
	"stat": true, "du": true, "tree": true, "which": true, "date": true,
	"dir": true, // Windows
}

// readOnlyGitSubcommands never take a mutating form.
var readOnlyGitSubcommands = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "blame": true,
}

// gitSubcommandIsReadOnly reports whether a git invocation's subcommand and
// arguments are provably read-only. "branch"/"remote" are only read-only
// with no arguments or a bare listing flag; any other argument (a
// branch/remote name, "-d/-D/-m/-M", "add", "set-url", "remove", "rename")
// can mutate the repository or redirect where a later push sends code.
func gitSubcommandIsReadOnly(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	sub := fields[1]
	if readOnlyGitSubcommands[sub] {
		return true
	}
	if sub == "branch" || sub == "remote" {
		rest := fields[2:]
		if len(rest) == 0 {
			return true
		}
		return len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--list" || rest[0] == "-a")
	}
	return false
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
			if res.Output != "" {
				b.WriteString(res.Output + "\n")
			}
			continue
		}
		b.WriteString(res.Output + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// CollapseBlocks replaces each fenced tool block in reply with a one-line
// description, for compact chat rendering (full bodies stay in the session
// and on the wire — this is display only).
func CollapseBlocks(reply string) string {
	lines := strings.Split(reply, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		open := fenceOpen.FindStringSubmatch(strings.TrimRight(lines[i], "\r"))
		if open == nil {
			out = append(out, lines[i])
			continue
		}
		closing := regexp.MustCompile("^`{" + fmt.Sprint(len(open[1])) + ",}[ \t]*$")
		var body []string
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if closing.MatchString(strings.TrimRight(lines[j], "\r")) {
				c := Call{Tool: open[2], Path: strings.TrimSpace(open[3]), Body: joinBody(body)}
				out = append(out, "⚒ "+c.Describe())
				i = j
				closed = true
				break
			}
			body = append(body, strings.TrimRight(lines[j], "\r"))
		}
		if !closed { // unterminated block: show it as-is
			out = append(out, lines[i:]...)
			break
		}
	}
	return strings.Join(out, "\n")
}

// CollapseResults renders a compact one-line-per-call view of a results
// message produced by FormatResults.
func CollapseResults(content string) string {
	var (
		out  []string
		name string
		body []string
	)
	flush := func() {
		if name != "" {
			out = append(out, "  ⎿ "+name+" → "+SummarizeOutput(strings.Join(body, "\n")))
		}
	}
	for _, l := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(l, "### "); ok {
			flush()
			name = strings.TrimSpace(rest)
			body = nil
			continue
		}
		if name != "" {
			body = append(body, l)
		}
	}
	flush()
	if len(out) == 0 {
		return SummarizeOutput(content)
	}
	return strings.Join(out, "\n")
}

// SummarizeOutput reduces one tool result to a single line: short outputs
// and errors show their text, long outputs just their line count.
func SummarizeOutput(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no output)"
	}
	lines := strings.Split(s, "\n")
	first := strings.TrimSpace(lines[0])
	if strings.HasPrefix(first, "error:") {
		if len(lines) > 1 {
			return truncateLine(first, 120) + fmt.Sprintf(" (+%d lines)", len(lines)-1)
		}
		return truncateLine(first, 120)
	}
	// Web tool outputs carry a summary-ready status as their first line.
	if strings.HasPrefix(first, "fetched ") || webResultsLine.MatchString(first) {
		return truncateLine(first, 120)
	}
	if len(lines) == 1 {
		return truncateLine(first, 100)
	}
	return fmt.Sprintf("%d lines of output", len(lines))
}

// webResultsLine matches the first line of a web_search result block.
var webResultsLine = regexp.MustCompile(`^(\d+ results|no results) for "`)

func truncateLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Describe renders one call for the approval prompt.
func (c Call) Describe() string {
	if c.MCPServer != "" {
		return fmt.Sprintf("%s: %s(%s)", c.MCPServer, c.MCPTool, truncateLine(c.MCPArgs, 80))
	}
	switch c.Tool {
	case ToolRunCommand:
		return "run: " + strings.TrimSpace(c.Body)
	case ToolWriteFile:
		return fmt.Sprintf("write %s (%d bytes)", c.Path, len(c.Body))
	case ToolWebSearch:
		return fmt.Sprintf("web_search(%q)", strings.TrimSpace(c.Body))
	case ToolWebFetch:
		return "fetch " + c.Path
	default:
		if c.Path == "" {
			return c.Tool
		}
		return c.Tool + " " + c.Path
	}
}

// Instructions is appended to the system prompt while tools are enabled;
// withWeb adds the web tools when the user has turned them on.
func Instructions(root string, withWeb bool) string {
	webTools, webRules := "", ""
	if withWeb {
		webTools = webFencedForms + "\n"
		webRules = "\n\n" + webInstructions
	}
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using tools.
To use a tool, emit a fenced code block whose info string is "tool <name> [path]". Available tools:

- list_dir [path] — list a directory (path optional, defaults to the project root)
- read_file <path> — return a file's contents
- write_file <path> — create or overwrite a file with the block's body
- run_command — run one shell command in the project directory; the command is the block's body
%s
Example — save a script, then a read-only command:

`+"```"+`tool write_file scripts/hello.sh
#!/bin/sh
echo hello
`+"```"+`

`+"```"+`tool run_command
grep -rn "TODO" scripts
`+"```"+`

Rules:
- Paths are always relative to the project root; never use absolute paths or "..".
- run_command takes exactly one command line; save multi-line scripts with write_file first.
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
- After you emit tool blocks, stop and wait: the results come back in the next user message, marked "%s".
- Use one block per action. If a body contains triple backticks, open the tool block with four.
- When the task is complete, reply normally without any tool blocks.%s`, root, webTools, ResultsPrefix, webRules))
}

// ErrDenied is the result error for calls the user rejected.
var ErrDenied = errors.New("denied by the user")

// DeniedResults builds the results message for a rejected batch.
func DeniedResults(calls []Call) []Result {
	out := make([]Result, len(calls))
	for i, c := range calls {
		out[i] = Result{Call: c, Err: ErrDenied}
	}
	return out
}
