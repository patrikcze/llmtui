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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/procutil"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// ResultsPrefix marks the follow-up message that carries tool output back to
// the model. The TUI also uses it to restyle those messages in the viewport.
const ResultsPrefix = "[tool results]"

// Known tool names.
const (
	ToolListDir      = "list_dir"
	ToolReadFile     = "read_file"
	ToolGlob         = "glob"
	ToolGrep         = "grep"
	ToolWriteFile    = "write_file"
	ToolRunCommand   = "run_command"
	ToolWebSearch    = "web_search"
	ToolWebFetch     = "web_fetch"
	ToolLocalContext = "local_context"
	ToolSearch       = "tool_search"
	// ToolAskUser pauses the current tool loop until the human supplies one
	// answer. The TUI controller handles the pause; Runner never executes it.
	ToolAskUser = "ask_user"
	// ToolSkillLoad activates a skill (declarative task instructions) for the
	// current agent run. It executes no code and grants no permissions: the
	// skill body is included by the prompt composer on the next inference.
	ToolSkillLoad = "skill_load"
	// ToolEditFile replaces one exact, unique text fragment in an existing
	// workspace file. It shares write_file's guardrails and approval class and
	// adds a stale-content precondition; it never creates a file.
	ToolEditFile = "edit_file"
	// ToolPersonalApps is the optional Apple Mail/Calendar integration. The
	// block body (fenced) or function arguments (native) are one JSON
	// envelope {"operation":"...","arguments":{...}} that internal/personalapps
	// decodes and validates in full; Runner does no parsing of its own beyond
	// a size check. See personal_apps.go.
	ToolPersonalApps = "personal_apps"
	// ToolGetEntityDetails is a read-only controller capability. The TUI
	// resolves it against its session-local entity registry.
	ToolGetEntityDetails = "get_entity_details"
)

// read_file optional line-range bounds. DefaultReadLimit applies when a range
// is requested with only an offset; MaxReadLimit is the hard ceiling so one
// call cannot pull an unbounded slice of a large file into model context.
const (
	DefaultReadLimit = 200
	MaxReadLimit     = 500
)

// CanonicalReadRange normalizes read_file's optional line range. ranged is
// false for a whole-file read (both arguments omitted or non-positive), which
// keeps the legacy behavior byte-for-byte. When ranged, start is the 1-based
// first line and count the bounded number of lines. It never reports an
// error — call ValidateReadRange first for the model-facing checks.
func CanonicalReadRange(offset, limit int) (start, count int, ranged bool) {
	if offset <= 0 && limit <= 0 {
		return 0, 0, false
	}
	start = offset
	if start < 1 {
		start = 1
	}
	count = limit
	if count <= 0 {
		count = DefaultReadLimit
	}
	if count > MaxReadLimit {
		count = MaxReadLimit
	}
	return start, count, true
}

// ValidateReadRange rejects a read_file range a model can correct from: a
// negative bound, or a limit past the hard ceiling. Omitted (zero) bounds are
// valid — they select the legacy whole-file read or the default line count.
func ValidateReadRange(offset, limit int) error {
	if offset < 0 {
		return fmt.Errorf("read_file offset must be 1 or greater (1-based line number)")
	}
	if limit < 0 {
		return fmt.Errorf("read_file limit must be 1 or greater")
	}
	if limit > MaxReadLimit {
		return fmt.Errorf("read_file limit %d exceeds the maximum of %d lines; page through the file with successive offset values", limit, MaxReadLimit)
	}
	return nil
}

// Call is one tool invocation: parsed from a fenced block in an assistant
// reply, or converted from a native function call (in which case ID is set
// and the results must go back as role:"tool" messages).
type Call struct {
	ID   string
	Tool string
	Path string
	Body string
	// Filter optionally narrows search tools (for example grep's file glob).
	Filter string
	// Offset and Limit are read_file's optional 1-based line range. Both zero
	// (or negative) means a whole-file read, preserving the legacy behavior.
	// See CanonicalReadRange / ValidateReadRange.
	Offset int
	Limit  int
	// OldText and NewText carry edit_file's single exact replacement. OldText
	// must match the target file exactly once; NewText may be empty (a
	// controlled deletion of that exact fragment).
	OldText string
	NewText string
	// InputErr records malformed native JSON arguments. The call remains in
	// the batch so the model receives a correlated tool error, but Execute
	// must not run a zero-valued approximation of the requested operation.
	InputErr string
	// Max caps web_search results (native max_results argument).
	Max int
	// Freshness is an explicit caller-supplied observation epoch for volatile
	// read tools. Reusing the same token remains the same operation; changing
	// it deliberately requests a new poll without disguising it through
	// incidental argument variation.
	Freshness string
	// ContextKind selects local_context's bounded read-only collector.
	ContextKind string
	// SearchQuery carries a local tool_search or get_entity_details query.
	SearchQuery string
	// EntityIDs and EntityLevel carry get_entity_details' controller-only
	// request. They are validated before the TUI resolves them.
	EntityIDs       [MaxEntityDetailsIDs]string
	EntityIDCount   int
	EntityLevel     string
	EntityKinds     [MaxEntityDetailsKinds]string
	EntityKindCount int
	// Question, Choices, and AllowText carry ask_user's bounded interaction
	// request. They are controller state, not approval or execution authority.
	Question    string
	Choices     [MaxAskUserChoices]string
	ChoiceCount int
	AllowText   bool

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
//
// Meta is the additive typed outcome/coverage/window envelope (see
// result.go). It is populated by every in-package producer and by the
// controller-only producers in internal/tui that build a Result directly
// (ask_user, tool_search, get_entity_details, MCP). A Result built before
// Phase 1a landed, or by a path this phase did not reach, carries a
// zero-value Meta (Meta.Outcome == "") — callers that read Meta must treat
// that as "not classified," not as OutcomeUnknown, which is a distinct,
// explicit value.
type Result struct {
	Call     Call
	Output   string
	Diff     string
	Err      error
	Entities []entity.Candidate
	Meta     ResultMeta
}

// fenceOpen matches a tool block opener: 3+ backticks, "tool", name, optional path.
var fenceOpen = regexp.MustCompile("^(`{3,})tool[ \t]+([A-Za-z0-9_.-]+)(?:[ \t]+(.+?))?[ \t]*$")

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
				call := Call{Tool: open[2], Path: strings.TrimSpace(open[3]), Body: joinBody(body)}
				if op := personalapps.Operation(call.Tool); op.Valid() {
					// Fenced personal_apps tools are named one-per-operation
					// (PersonalAppsFencedForms), matching native tool-calling's
					// own one-per-operation schema, so a model calls them like
					// every other fenced tool. internal/personalapps.ParseRequest
					// still requires the combined {"operation","arguments"}
					// envelope; that reshaping happens here, once, built from
					// data this file fully controls — the tool name the fence
					// marker itself named — never inferred from the model's own
					// body shape. See personalAppsEnvelope's own doc comment for
					// why that distinction matters.
					body := call.Body
					call.Path = ""
					if len(body) > MaxPersonalAppsPayloadBytes {
						call.InputErr = fmt.Sprintf("%s arguments exceed the %d byte limit", op, MaxPersonalAppsPayloadBytes)
					} else if env, err := personalAppsEnvelope(op, body); err != nil {
						call.InputErr = fmt.Sprintf("%s arguments are not valid JSON: %v", op, err)
					} else {
						call.Body = env
					}
					call.Tool = ToolPersonalApps
				} else {
					switch call.Tool {
					case ToolAskUser:
						decodeAskUserBody(&call)
					case ToolLocalContext:
						decodeLocalContextBody(&call)
					case ToolSearch:
						decodeToolSearchBody(&call)
					case ToolGetEntityDetails:
						decodeEntityDetailsBody(&call)
					case ToolReadFile:
						decodeReadFileBody(&call)
					case ToolEditFile:
						decodeEditFileBody(&call)
					case ToolPersonalApps:
						decodePersonalAppsBody(&call)
					}
				}
				if server, tool, ok := SplitMCPToolName(call.Tool); ok {
					call.MCPServer, call.MCPTool = server, tool
					call.MCPArgs = strings.TrimSpace(call.Body)
					if call.MCPArgs == "" {
						call.MCPArgs = "{}"
					}
				}
				calls = append(calls, call)
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
	if len(calls) == 0 {
		if call := parseGemmaFallbackPersonalAppsCall(reply); call != nil {
			calls = append(calls, *call)
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
	// LocalContext collects bounded machine/workspace facts without network
	// access. Tests replace it with a fixture collector.
	LocalContext LocalContextCollector

	// PersonalApps enables the personal_apps tool (optional Apple
	// Mail/Calendar integration) when non-nil, mirroring Web. The Runner
	// itself decides nothing about scope, connection, or approval — every
	// one of those decisions lives in the Service and is re-checked there
	// on every call, so this field only wires the entry point.
	PersonalApps PersonalAppsService
}

// PersonalAppsService is what the runner needs from
// internal/personalapps.Service: parse and execute one raw request against
// its own configured scope, limits, and adapters. The interface exists so
// tests can stub it without constructing a real Service, and so this
// package never imports personalapps.Options or its adapter interfaces.
type PersonalAppsService interface {
	ExecuteRaw(ctx context.Context, raw []byte) personalapps.Result
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
		LocalContext:   NewLocalContextCollector(root),
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
		res.Err = withCode(fmt.Errorf("invalid arguments for %s: %s", c.Tool, c.InputErr), "invalid_arguments", RetryCorrectInput)
		res.Meta = finalizeMeta(ResultMeta{Effect: EffectNone}, res.Err)
		return res
	}
	var meta ResultMeta
	switch c.Tool {
	case ToolListDir:
		res.Output, meta, res.Err = r.listDir(c.Path)
	case ToolReadFile:
		res.Output, meta, res.Err = r.readFileMeta(c.Path, c.Offset, c.Limit)
		if res.Err == nil && !IsSecretPath(c.Path) {
			res.Entities = []entity.Candidate{fileEntityCandidate(c, res.Output)}
		}
	case ToolEditFile:
		res.Output, res.Diff, meta, res.Err = r.editFile(c.Path, c.OldText, c.NewText)
	case ToolGlob:
		res.Output, meta, res.Err = r.globFiles(ctx, c.Path, c.Body)
	case ToolGrep:
		res.Output, meta, res.Err = r.grepFiles(ctx, c.Path, c.Body, c.Filter)
	case ToolWriteFile:
		res.Output, res.Diff, meta, res.Err = r.writeFileMeta(c.Path, c.Body)
	case ToolRunCommand:
		res.Output, meta, res.Err = r.runCommandContext(ctx, c.Body)
	case ToolWebSearch:
		res.Output, res.Entities, meta, res.Err = r.webSearch(ctx, c)
	case ToolWebFetch:
		res.Output, res.Entities, meta, res.Err = r.webFetch(ctx, c)
	case ToolSkillLoad:
		res.Output, meta, res.Err = r.skillLoad(c)
	case ToolAskUser:
		res.Err = errors.New("ask_user is a controller pause and cannot be executed by the tool runner")
		meta.Effect = EffectNone
	case ToolLocalContext:
		res.Output, meta, res.Err = r.localContext(ctx, c)
	case ToolSearch:
		res.Err = errors.New("tool_search is handled by the controller and cannot be executed by the tool runner")
		meta.Effect = EffectNone
	case ToolGetEntityDetails:
		res.Err = errors.New("get_entity_details is handled by the controller and cannot be executed by the tool runner")
		meta.Effect = EffectNone
	case ToolPersonalApps:
		res.Output, meta, res.Err = r.personalApps(ctx, c)
	default:
		res.Err = withCode(fmt.Errorf("%w %q (built-in: %s, %s, %s, %s, %s, %s, %s, %s, %s)",
			ErrUnknownTool, c.Tool, ToolListDir, ToolReadFile, ToolGlob, ToolGrep, ToolWriteFile, ToolEditFile, ToolRunCommand, ToolWebSearch, ToolWebFetch), "not_found", RetryCorrectInput)
		meta.Effect = EffectNone
	}
	res.Meta = finalizeMeta(meta, res.Err)
	return res
}

func fileEntityCandidate(c Call, output string) entity.Candidate {
	return entity.Candidate{
		Kind: entity.KindFile,
		Provenance: entity.Provenance{
			Source:    "workspace",
			Operation: ToolReadFile,
			Reference: c.Path,
			CallID:    c.ID,
		},
		Label:    c.Path,
		Metadata: entity.Metadata{Path: c.Path, SizeBytes: len(output)},
		Trust:    entity.TrustWorkspaceUntrusted,
		Scope:    entity.ScopeSession,
		Payload:  output,
	}
}

// ErrUnknownTool marks a call whose tool name matched nothing. Callers that
// know about additional tools (the TUI's MCP integration) detect it with
// errors.Is and append their own tool names, so the model is never told the
// built-ins are the complete set when they aren't — a model that mangles an
// MCP name (e.g. "mcp_srv_tool" for "mcp__srv__tool") must see the correct
// names to self-correct instead of concluding the tools don't exist.
var ErrUnknownTool = errors.New("unknown tool")

const maxDirEntries = 200

func (r *Runner) listDir(rel string) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	abs, err := r.resolve(rel)
	if err != nil {
		return "", meta, withCode(err, "safety_block", RetryCorrectInput)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("list directory: %w", err), "not_found", RetryCorrectInput)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	capped := false
	for i, e := range entries {
		if i >= maxDirEntries {
			fmt.Fprintf(&b, "… and %d more entries\n", len(entries)-maxDirEntries)
			capped = true
			break
		}
		if e.IsDir() {
			b.WriteString(e.Name() + "/\n")
		} else {
			b.WriteString(e.Name() + "\n")
		}
	}
	// list_dir always reports OutcomeOK: the cap is a documented, intentional
	// bound on an otherwise-complete listing, not a failure to inspect the
	// directory (every entry was read to produce the count in "and N more").
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{
		SourceComplete:  !capped,
		CaptureComplete: !capped,
		PreviewComplete: true,
		TotalLines:      int64Ptr(int64(len(entries))),
	}
	if capped {
		meta.Coverage.Reasons = []string{"entries"}
	}
	if b.Len() == 0 {
		return "(empty directory)", meta, nil
	}
	return strings.TrimRight(b.String(), "\n"), meta, nil
}

func (r *Runner) readFile(rel string, offset, limit int) (string, error) {
	output, _, err := r.readFileMeta(rel, offset, limit)
	return output, err
}

func (r *Runner) readFileMeta(rel string, offset, limit int) (output string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	if rel == "" {
		return "", meta, withCode(fmt.Errorf("read_file needs a path"), "invalid_arguments", RetryCorrectInput)
	}
	if verr := ValidateReadRange(offset, limit); verr != nil {
		return "", meta, withCode(verr, "invalid_arguments", RetryCorrectInput)
	}
	if _, rerr := r.resolve(rel); rerr != nil {
		return "", meta, withCode(rerr, "safety_block", RetryCorrectInput)
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	name := filepath.Clean(rel)
	info, err := root.Stat(name)
	if err != nil {
		return "", meta, withCode(fmt.Errorf("read file: %w", err), "not_found", RetryCorrectInput)
	}
	if info.IsDir() {
		return "", meta, withCode(fmt.Errorf("%q is a directory (use list_dir)", rel), "invalid_arguments", RetryCorrectInput)
	}
	if !info.Mode().IsRegular() {
		return "", meta, withCode(fmt.Errorf("%q is not a regular file", rel), "unsupported_content", RetryCorrectInput)
	}
	byteLimit := int64(r.maxKB) * 1024
	file, err := root.Open(name)
	if err != nil {
		return "", meta, fmt.Errorf("read file: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", meta, fmt.Errorf("read file metadata: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return "", meta, withCode(fmt.Errorf("%q is not a regular file", rel), "unsupported_content", RetryCorrectInput)
	}
	// A bounded read is enough for both modes: the whole-file read is capped
	// at byteLimit as before, and a line range is sliced out of that same
	// bounded prefix (io.LimitReader also contains a pathological single
	// multi-gigabyte line).
	data, err := io.ReadAll(io.LimitReader(file, byteLimit+1))
	if err != nil {
		return "", meta, fmt.Errorf("read file: %w", err)
	}
	bytesTruncated := int64(len(data)) > byteLimit
	if bytesTruncated {
		data = data[:byteLimit]
	}

	start, count, ranged := CanonicalReadRange(offset, limit)
	if !ranged {
		text, consumed := boundedUTF8(data, int(byteLimit))
		cov := Coverage{
			SourceComplete:  !bytesTruncated,
			CaptureComplete: !bytesTruncated,
			PreviewComplete: consumed >= len(data),
			ObservedBytes:   int64(len(data)),
			RetainedBytes:   int64(consumed),
		}
		if !bytesTruncated {
			cov.TotalBytes = int64Ptr(openedInfo.Size())
		}
		outcome := OutcomeOK
		if bytesTruncated {
			cov.Reasons = []string{"bytes"}
			outcome = OutcomePartial
		}
		meta.Outcome, meta.Coverage = outcome, cov
		if bytesTruncated || consumed < len(data) {
			total := openedInfo.Size()
			if bytesTruncated && total < int64(len(data))+1 {
				total = int64(len(data)) + 1
			}
			return text + fmt.Sprintf("\n… truncated (%d of %d bytes shown)", consumed, total), meta, nil
		}
		return text, meta, nil
	}
	text, lineMeta, err := renderLineRange(filepath.ToSlash(name), data, bytesTruncated, start, count, int(byteLimit))
	lineMeta.Effect = EffectNone
	if err != nil {
		return "", lineMeta, withCode(err, "range_after_eof", RetryCorrectInput)
	}
	return text, lineMeta, nil
}

// renderLineRange slices [start, start+count) 1-based lines out of the bounded
// file bytes, verbatim, with one compact header line and no per-line numbers
// (so the model cannot copy an artificial number into an edit_file old_text).
// An offset past the last available line is a recoverable error, never a
// silent empty success.
func renderLineRange(displayPath string, data []byte, bytesTruncated bool, start, count, byteLimit int) (string, ResultMeta, error) {
	var meta ResultMeta
	segments := splitKeepNewline(data)
	totalKnown := !bytesTruncated
	if start > len(segments) {
		if totalKnown {
			return "", meta, fmt.Errorf("read_file offset %d is past the end of %q (%d lines)", start, displayPath, len(segments))
		}
		return "", meta, fmt.Errorf("read_file offset %d is past the %d lines that fit within the %d KB read limit for %q", start, len(segments), byteLimit/1024, displayPath)
	}
	first := start - 1
	last := first + count
	if last > len(segments) {
		last = len(segments)
	}
	selected := bytes.Join(segments[first:last], nil)
	text, consumed := boundedUTF8(selected, byteLimit)
	lineCapped := consumed < len(selected)

	var header strings.Builder
	fmt.Fprintf(&header, "[read_file: %s lines %d-%d", displayPath, start, last)
	if totalKnown {
		fmt.Fprintf(&header, " of %d", len(segments))
	}
	switch {
	case last < len(segments):
		fmt.Fprintf(&header, ", next_offset=%d]", last+1)
	case bytesTruncated:
		fmt.Fprintf(&header, ", more of the file is past the %d KB read limit]", byteLimit/1024)
	case lineCapped:
		fmt.Fprintf(&header, ", line %d truncated at the %d KB limit]", last, byteLimit/1024)
	case totalKnown:
		header.WriteString(", end of file]")
	default:
		header.WriteString("]")
	}

	// SourceComplete tracks only the byte-prefix read cap (bytesTruncated), not
	// whether more of the file exists past this requested window — a ranged
	// read that returns exactly the window the caller asked for is a complete,
	// successful bounded observation (OutcomeOK), not a partial one. NextOffset
	// exists precisely so pagination is navigation, not incompleteness.
	cov := Coverage{
		SourceComplete:  !bytesTruncated,
		CaptureComplete: !bytesTruncated,
		PreviewComplete: !lineCapped,
		ObservedBytes:   int64(len(data)),
		RetainedBytes:   int64(consumed),
	}
	if totalKnown {
		cov.TotalLines = int64Ptr(int64(len(segments)))
	}
	var reasons []string
	if bytesTruncated {
		reasons = append(reasons, "bytes")
	}
	if lineCapped {
		reasons = append(reasons, "line")
	}
	cov.Reasons = reasons
	outcome := OutcomeOK
	if bytesTruncated {
		outcome = OutcomePartial
	}
	win := &Window{StartLine: int64(start), EndLine: int64(last), PartialLine: lineCapped}
	if last < len(segments) {
		win.NextOffset = int64Ptr(int64(last + 1))
	}
	meta = ResultMeta{Outcome: outcome, Coverage: cov, Window: win}
	if text == "" {
		return header.String(), meta, nil
	}
	return header.String() + "\n\n" + text, meta, nil
}

// splitKeepNewline splits file bytes into line segments that each retain their
// trailing "\n", so rejoining a slice reproduces the original bytes exactly. A
// final empty segment after a trailing newline is dropped: "a\nb\n" is two
// lines, not three.
func splitKeepNewline(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	segments := bytes.SplitAfter(data, []byte{'\n'})
	if n := len(segments); n > 0 && len(segments[n-1]) == 0 {
		segments = segments[:n-1]
	}
	return segments
}

// boundedUTF8 converts arbitrary file bytes into valid UTF-8 without letting
// replacement runes or a split final rune expand the returned content beyond
// maxBytes. consumed reports source bytes represented in the result.
func boundedUTF8(data []byte, maxBytes int) (text string, consumed int) {
	if maxBytes <= 0 || len(data) == 0 {
		return "", 0
	}
	var b strings.Builder
	b.Grow(min(len(data), maxBytes))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		encoded := string(r)
		if b.Len()+len(encoded) > maxBytes {
			break
		}
		b.WriteString(encoded)
		consumed += size
		data = data[size:]
	}
	return b.String(), consumed
}

func (r *Runner) writeFile(rel, content string) (output, diff string, err error) {
	output, diff, _, err = r.writeFileMeta(rel, content)
	return output, diff, err
}

func (r *Runner) writeFileMeta(rel, content string) (output, diff string, meta ResultMeta, err error) {
	diff, meta, err = r.writeFileChecked(rel, content, nil)
	if err != nil {
		return "", "", meta, err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))), diff, meta, nil
}

// writeFileChecked is the shared safe-write implementation behind both
// write_file and edit_file: workspace confinement, blocked-path guardrails,
// the size cap, parent-directory creation, the O_TRUNC write, and the
// display diff. It returns only the diff; callers format their own result
// line.
//
// When expectCurrent is non-nil the write is a surgical edit: the file must
// already exist, be readable within the cap, and hold exactly the bytes the
// edit was computed against. Any mismatch fails the write untouched so a
// concurrent external change is never silently clobbered.
func (r *Runner) writeFileChecked(rel, content string, expectCurrent *string) (diff string, meta ResultMeta, err error) {
	// The write either fully replaces the file's content or fails outright —
	// there is no partial-content mechanism to be incomplete about.
	meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(content)), RetainedBytes: int64(len(content))}
	meta.Effect = EffectNone // nothing attempted yet at every early-return below
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", meta, withCode(fmt.Errorf("write_file needs a path"), "invalid_arguments", RetryCorrectInput)
	}
	rel = filepath.Clean(rel)
	displayPath := filepath.ToSlash(rel)
	// Block writes into .git (a hook would execute on the next git command),
	// key-material directories, and shell startup files.
	if msg := r.Guardrails.checkWritePath(rel); msg != "" {
		return "", meta, withCode(errors.New(msg), "safety_block", RetryCorrectInput)
	}
	if len(content) > r.maxKB*1024 {
		return "", meta, withCode(fmt.Errorf("content exceeds the %d KB write limit", r.maxKB), "unsupported_content", RetryCorrectInput)
	}
	if _, rerr := r.resolve(rel); rerr != nil {
		return "", meta, withCode(rerr, "safety_block", RetryCorrectInput)
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	// Capture the previous content so the TUI can show what changed.
	existed := false
	oldContent := ""
	oldTooBig := false
	if info, err := root.Stat(rel); err == nil {
		if info.IsDir() {
			return "", meta, withCode(fmt.Errorf("%q is a directory", rel), "invalid_arguments", RetryCorrectInput)
		}
		existed = true
		if info.Size() <= int64(r.maxKB)*1024 {
			if data, rerr := readRootFileLimited(root, rel, int64(r.maxKB)*1024); rerr == nil {
				oldContent = string(data)
			} else {
				oldTooBig = true // unreadable: treat like undiffable
			}
		} else {
			oldTooBig = true
		}
	}
	if expectCurrent != nil {
		if !existed {
			return "", meta, withCode(fmt.Errorf("%q no longer exists; use write_file to create it", displayPath), "not_found", RetryReread)
		}
		if oldTooBig {
			return "", meta, withCode(fmt.Errorf("%q changed and is no longer readable within the %d KB limit; re-read it and retry", displayPath, r.maxKB), "unsupported_content", RetryReread)
		}
		if oldContent != *expectCurrent {
			return "", meta, withCode(fmt.Errorf("%q changed since it was read; re-read the file and retry the edit against its current text", displayPath), "match_not_found", RetryReread)
		}
	}
	// From here on, a failure occurs while a write may already be underway —
	// its effect on disk is genuinely unknown, not "none".
	meta.Effect = EffectUnknown
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return "", meta, fmt.Errorf("create parent directory: %w", err)
	}
	file, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", meta, fmt.Errorf("open file for writing: %w", err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		_ = file.Close()
		return "", meta, fmt.Errorf("write file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", meta, fmt.Errorf("close written file: %w", err)
	}
	meta.Outcome = OutcomeOK
	meta.Effect = EffectChanged
	if oldTooBig {
		return fmt.Sprintf("Update(%s) — previous content replaced (too large to diff)", displayPath), meta, nil
	}
	rendered := RenderWriteDiff(displayPath, oldContent, content, existed)
	if IsNoChangeDiff(rendered) {
		meta.Effect = EffectUnchanged
	}
	return rendered, meta, nil
}

// editFile performs exactly one literal, exact-match replacement in an
// existing text file. It never creates a file, never uses regex or fuzzy
// matching, and fails without writing when old_text is absent or matches more
// than once — the model is expected to re-read and retry with unique context.
func (r *Runner) editFile(rel, oldText, newText string) (output, diff string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", meta, withCode(fmt.Errorf("edit_file needs a path"), "invalid_arguments", RetryCorrectInput)
	}
	if oldText == "" {
		return "", "", meta, withCode(fmt.Errorf("edit_file needs old_text — the exact fragment to replace"), "invalid_arguments", RetryCorrectInput)
	}
	if oldText == newText {
		return "", "", meta, withCode(fmt.Errorf("edit_file old_text and new_text are identical; nothing to change"), "no_change", RetryCorrectInput)
	}
	rel = filepath.Clean(rel)
	displayPath := filepath.ToSlash(rel)
	if msg := r.Guardrails.checkWritePath(rel); msg != "" {
		return "", "", meta, withCode(errors.New(msg), "safety_block", RetryCorrectInput)
	}
	if _, rerr := r.resolve(rel); rerr != nil {
		return "", "", meta, withCode(rerr, "safety_block", RetryCorrectInput)
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", "", meta, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, statErr := root.Stat(rel)
	if errors.Is(statErr, os.ErrNotExist) {
		return "", "", meta, withCode(fmt.Errorf("%q does not exist; edit_file only changes existing files — use write_file to create one", displayPath), "not_found", RetryCorrectInput)
	}
	if statErr != nil {
		return "", "", meta, fmt.Errorf("edit file: %w", statErr)
	}
	if info.IsDir() {
		return "", "", meta, withCode(fmt.Errorf("%q is a directory", displayPath), "invalid_arguments", RetryCorrectInput)
	}
	if !info.Mode().IsRegular() {
		return "", "", meta, withCode(fmt.Errorf("%q is not a regular file", displayPath), "unsupported_content", RetryCorrectInput)
	}
	byteLimit := int64(r.maxKB) * 1024
	data, err := readRootFileLimited(root, rel, byteLimit)
	if err != nil {
		return "", "", meta, withCode(fmt.Errorf("%q is larger than the %d KB edit limit; use write_file to replace it wholesale", displayPath, r.maxKB), "unsupported_content", RetryCorrectInput)
	}
	if !utf8.Valid(data) {
		return "", "", meta, withCode(fmt.Errorf("%q is not valid UTF-8 text; edit_file only edits text files", displayPath), "encoding_loss", RetryCorrectInput)
	}
	current := string(data)
	switch matches := strings.Count(current, oldText); {
	case matches == 0:
		return "", "", meta, withCode(fmt.Errorf("old_text was not found exactly in %q; re-read the file (or a line range of it) and retry with its current text", displayPath), "match_not_found", RetryReread)
	case matches > 1:
		return "", "", meta, withCode(fmt.Errorf("old_text matches %d places in %q; include more surrounding context so it identifies exactly one location", matches, displayPath), "ambiguous_match", RetryCorrectInput)
	}
	updated := strings.Replace(current, oldText, newText, 1)
	if int64(len(updated)) > byteLimit {
		return "", "", meta, withCode(fmt.Errorf("the edited %q would exceed the %d KB write limit", displayPath, r.maxKB), "unsupported_content", RetryCorrectInput)
	}
	diff, meta, err = r.writeFileChecked(rel, updated, &current)
	if err != nil {
		return "", "", meta, err
	}
	return fmt.Sprintf("edited %s: replaced 1 exact occurrence", displayPath), diff, meta, nil
}

func readRootFileLimited(root *os.Root, name string, limit int64) (data []byte, err error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file changed while reading and exceeds %d bytes", limit)
	}
	return data, nil
}

// runCommand executes one shell command in the workspace directory. The
// shell is picked per OS (sh on Unix, cmd on Windows), output is size-capped,
// execution is time-limited, and the environment is sanitized so secrets in
// the parent process never reach the command (or, through its output, the
// model).
func (r *Runner) runCommand(body string) (string, error) {
	output, _, err := r.runCommandContext(context.Background(), body)
	return output, err
}

func (r *Runner) runCommandContext(parent context.Context, body string) (string, ResultMeta, error) {
	// run_command runs an arbitrary shell command: even a "successful" run
	// (exit 0) may have changed the workspace, and this package has no way
	// to know either way, so Effect is always unknown — never inferred as
	// "none" or "changed" from the exit code or output alone.
	meta := ResultMeta{Effect: EffectUnknown}
	cmdline := strings.TrimSpace(body)
	if cmdline == "" {
		return "", meta, withCode(fmt.Errorf("run_command needs a command in the block body"), "invalid_arguments", RetryCorrectInput)
	}
	if strings.ContainsAny(cmdline, "\n\r") {
		return "", meta, withCode(fmt.Errorf("one command per block — multi-line scripts must be saved with write_file first"), "invalid_arguments", RetryCorrectInput)
	}
	if commandReferencesOutsideWorkspace(cmdline, r.root) {
		return "", meta, withCode(fmt.Errorf("run_command blocked: command references a path outside the workspace"), "safety_block", RetryCorrectInput)
	}
	execLine, gitEnv := hardenGitInvocation(cmdline)

	timeout := r.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", execLine)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", execLine)
	}
	cmd.Dir = r.root
	cmd.Env = append(sanitizedEnv(os.Environ()), gitEnv...)
	procutil.SetupProcAttr(cmd)
	// A descendant retaining stdout/stderr must not keep CombinedOutput
	// blocked indefinitely after the context kills the direct shell.
	cmd.WaitDelay = time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", meta, fmt.Errorf("start command: %w", err)
	}
	if err := procutil.TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", meta, fmt.Errorf("contain command process tree: %w", err)
	}
	err := cmd.Wait()
	// Commands are synchronous by contract (one command per block), so any
	// process still in the group — a backgrounded `cmd &`, a timed-out tree —
	// must not outlive the tool call.
	procutil.KillGroup(cmd)
	observed := out.Len()
	output := strings.TrimRight(out.String(), "\n")
	capped := false
	if limit := r.maxKB * 1024; len(output) > limit {
		output, _ = terminaltext.TruncateBytes(output, limit)
		output += "\n… output truncated"
		capped = true
	}
	meta.Coverage = Coverage{
		SourceComplete:  !capped,
		CaptureComplete: !capped,
		PreviewComplete: true,
		ObservedBytes:   int64(observed),
		RetainedBytes:   int64(len(output)),
	}
	if capped {
		meta.Coverage.Reasons = []string{"bytes"}
	}
	// run_command's own Outcome states are OK/Failed/Timeout/Cancelled — never
	// Partial: an output cap is a capture-completeness fact (Coverage), not a
	// downgrade of whether the command itself succeeded.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		meta.Outcome = OutcomeTimeout
		meta.Error = &ErrorInfo{Code: "timeout", Retry: RetryLater, Message: boundErrorMessage(fmt.Errorf("command timed out after %s", timeout))}
		return output, meta, fmt.Errorf("command timed out after %s", timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		meta.Outcome = OutcomeCancelled
		meta.Error = &ErrorInfo{Code: "cancelled", Retry: RetryNone, Message: boundErrorMessage(fmt.Errorf("command cancelled: %w", ctx.Err()))}
		return output, meta, fmt.Errorf("command cancelled: %w", ctx.Err())
	}
	if err != nil {
		meta.Outcome = OutcomeFailed
		return output, meta, fmt.Errorf("command failed: %w", err)
	}
	meta.Outcome = OutcomeOK
	if output == "" {
		output = "(no output)"
	}
	return output, meta, nil
}

// skillLoad activates a skill for the current run via the configured
// SkillLoader. It is deliberately side-effect free beyond prompt state:
// unknown IDs and validation failures come back as recoverable tool errors
// the model can correct from.
func (r *Runner) skillLoad(c Call) (string, ResultMeta, error) {
	id := strings.TrimSpace(c.Path)
	if id == "" {
		id = strings.TrimSpace(c.Body)
	}
	if id == "" {
		return "", ResultMeta{Effect: EffectNone}, withCode(fmt.Errorf("skill_load needs a skill id"), "invalid_arguments", RetryCorrectInput)
	}
	if r.Skills == nil {
		return "", ResultMeta{Effect: EffectNone}, withCode(fmt.Errorf("skills are not available in this session"), "unsupported_content", RetryNone)
	}
	output, err := r.Skills.LoadSkillForRun(id)
	if err != nil {
		return "", ResultMeta{Effect: EffectNone}, withCode(err, "not_found", RetryCorrectInput)
	}
	// Activating a skill mutates run-local prompt-composition state (the
	// skill becomes active for the rest of the run) even though it writes no
	// workspace file, so Effect is "changed" rather than "none".
	meta := ResultMeta{
		Outcome:  OutcomeOK,
		Effect:   EffectChanged,
		Coverage: Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(output)), RetainedBytes: int64(len(output))},
	}
	return output, meta, nil
}

// secretEnvPattern matches environment variable names that likely hold
// credentials; those never reach commands the model runs.
var secretEnvPattern = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential|passphrase|(^|_)pass(_|$)|(^|_)(url|dsn)(_|$)|conn(ection)?_?string)`)

// blockedCommandEnvNames contains variables that expose credentials or can
// inject extra executable behavior into an otherwise auto-approved command.
var blockedCommandEnvNames = map[string]bool{
	"SSH_AUTH_SOCK":       true,
	"KUBECONFIG":          true,
	"VAULT_ADDR":          true,
	"RIPGREP_CONFIG_PATH": true,
}

// gitReadOnlyConfigOverrides neutralizes repository-local Git configuration
// keys that name an executable helper and that a genuinely read-only
// status/log/diff/show/blame invocation never needs. A supplied working tree
// can set these in its accompanying .git/config (a normal clone never
// transports .git/config, but a full checkout handed to the agent can carry
// an attacker-modified one); an auto-approved "read-only" git command would
// otherwise launch the configured helper with the user's privileges.
// diff.external is deliberately not here: an empty override value still
// counts as "external diff configured" and git fails the whole command
// ("external diff died") instead of falling back to its built-in diff, so it
// is neutralized with the --no-ext-diff flag in hardenGitInvocation instead.
// See the 2026-09-13 security review, finding 1 (CWE-78), reopening SEC-003
// ("git subcommand bypass", docs/architecture/v1-security-review.md).
var gitReadOnlyConfigOverrides = [][2]string{
	{"core.fsmonitor", ""},
	{"interactive.diffFilter", ""},
	{"core.pager", "cat"},
}

// gitHardenedEnv returns GIT_CONFIG_COUNT/KEY_n/VALUE_n environment
// variables that override gitReadOnlyConfigOverrides. These environment
// overrides take the same "above every config file" precedence as -c
// command-line options, so they win over repo-local, global, and system Git
// configuration regardless of which one names the helper.
func gitHardenedEnv() []string {
	env := make([]string, 0, len(gitReadOnlyConfigOverrides)*2+1)
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(gitReadOnlyConfigOverrides)))
	for i, kv := range gitReadOnlyConfigOverrides {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]),
		)
	}
	return env
}

// gitNoExtDiffSubcommands are read-only git subcommands that can render a
// diff and therefore consult diff.external/GIT_EXTERNAL_DIFF unless told not
// to; gitNoTextconvSubcommands additionally/instead consult an
// attribute-assigned textconv filter (gitattributes(5)), whose name comes
// from tracked .gitattributes content and so cannot be neutralized by a
// fixed config-key override the way gitReadOnlyConfigOverrides handles
// core.fsmonitor. Only the documented "--no-ext-diff"/"--no-textconv" flags
// disable them for a given invocation.
var (
	gitNoExtDiffSubcommands  = map[string]bool{"diff": true, "show": true, "log": true}
	gitNoTextconvSubcommands = map[string]bool{"diff": true, "show": true, "log": true, "blame": true}
)

// hardenGitInvocation returns the command line to actually execute and any
// extra environment variables to run it with. It applies only to a "git"
// command whose subcommand/arguments are already provably read-only
// (gitSubcommandIsReadOnly — the same shape ClassifyCommand lets run
// automatically) and that carries no shell syntax making re-tokenizing it
// unsafe (the same metacharacter and quoting shapes ClassifyCommand always
// sends to approval); the same shape check also means the CLI-flag rewrite
// below can never touch an explicitly-approved mutating command such as
// "git push", which may legitimately depend on credential.helper. Everything
// else is returned unchanged with no extra environment: a command still
// requiring human approval is shown and run exactly as written, never
// silently rewritten.
func hardenGitInvocation(cmdline string) (execLine string, env []string) {
	if strings.ContainsAny(cmdline, "\n\r|;&<>`$\\%^!()*?[]{}\"'") {
		return cmdline, nil
	}
	fields := strings.Fields(cmdline)
	if len(fields) == 0 || fields[0] != "git" || !gitSubcommandIsReadOnly(fields) {
		return cmdline, nil
	}
	env = gitHardenedEnv()
	sub := fields[1]
	var extra []string
	hasFlag := func(name string) bool {
		for _, f := range fields[2:] {
			if f == name {
				return true
			}
		}
		return false
	}
	if gitNoExtDiffSubcommands[sub] && !hasFlag("--no-ext-diff") && !hasFlag("--ext-diff") {
		extra = append(extra, "--no-ext-diff")
	}
	if gitNoTextconvSubcommands[sub] && !hasFlag("--no-textconv") && !hasFlag("--textconv") {
		extra = append(extra, "--no-textconv")
	}
	if len(extra) == 0 {
		return cmdline, env
	}
	rewritten := make([]string, 0, len(fields)+len(extra))
	rewritten = append(rewritten, fields[0], sub)
	rewritten = append(rewritten, extra...)
	rewritten = append(rewritten, fields[2:]...)
	return strings.Join(rewritten, " "), env
}

func sanitizedEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "LLMTUI_") || blockedCommandEnvNames[strings.ToUpper(name)] || secretEnvPattern.MatchString(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// maxAutoWebSearchQueryBytes bounds a search query that may leave the machine
// without the user confirming it.
const maxAutoWebSearchQueryBytes = 200

// maxAutoWebSearchTokenBytes bounds one whitespace-delimited token inside an
// auto-approved query. Natural-language search terms are short; a single long
// opaque run is a credential or an encoded payload, not a search.
const maxAutoWebSearchTokenBytes = 64

// webSearchNeedsApproval reports whether a search query must be confirmed
// before it leaves the machine. web_search is the one tool that combines
// unapproved execution with model-authored outbound content: read_file, grep,
// and list_dir also run unapproved, so a model that has been steered by
// injected repository or web content can read workspace data and place it in
// a query. web_fetch is already approval-gated and SSRF-guarded, which left
// search as the weaker sibling.
//
// Ordinary searches (short natural-language questions) stay automatic — the
// gate targets the shapes a genuine query never has: bulk length, embedded
// newlines, or a single very long opaque token.
func webSearchNeedsApproval(query string) bool {
	query = strings.TrimSpace(query)
	if len(query) > maxAutoWebSearchQueryBytes {
		return true
	}
	if strings.ContainsAny(query, "\n\r") {
		return true
	}
	for _, field := range strings.Fields(query) {
		if len(field) > maxAutoWebSearchTokenBytes {
			return true
		}
	}
	return false
}

// NeedsApproval reports whether a call must be confirmed under this runner's
// guardrail policy. read_file and grep of a likely secret file (.env, *.pem,
// id_rsa, …) ask first when RequireApprovalForSecretReads is on.
func (r *Runner) NeedsApproval(c Call) bool {
	switch c.Tool {
	case ToolListDir, ToolGlob, ToolSkillLoad, ToolAskUser:
		return false
	case ToolLocalContext:
		return strings.EqualFold(strings.TrimSpace(c.ContextKind), LocalContextClipboard)
	case ToolSearch:
		return false
	case ToolGetEntityDetails:
		return false
	case ToolWebSearch:
		return webSearchNeedsApproval(c.Body)
	case ToolReadFile, ToolGrep:
		return r.Guardrails.RequireApprovalForSecretReads && IsSecretPath(c.Path)
	case ToolRunCommand:
		return r.Guardrails.ClassifyCommand(c.Body, r.root).Verdict != VerdictAuto
	case ToolPersonalApps:
		// Reads are gated by the human's earlier explicit /personal-apps
		// connect, exactly like a connected MCP server's tools; a mutation
		// (change_apply) always needs approval and is additionally forced
		// past the /tools auto shortcut and any standing capability grant
		// in internal/tui's callNeedsApproval, since this is a
		// personal-data feature auto mode was never meant to cover.
		return personalapps.PeekOperation([]byte(c.Body)).Effect() == personalapps.EffectMutate
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
	case ToolAskUser:
		return "ask_user: " + c.Question
	case ToolLocalContext:
		return "local_context: " + c.ContextKind
	case ToolSearch:
		return fmt.Sprintf("tool_search: %q", c.SearchQuery)
	case ToolGetEntityDetails:
		if c.SearchQuery != "" {
			return fmt.Sprintf("get_entity_details: %q", c.SearchQuery)
		}
		return fmt.Sprintf("get_entity_details: %d %s", c.EntityIDCount, c.EntityLevel)
	case ToolRunCommand:
		return "run: " + strings.TrimSpace(c.Body)
	case ToolWriteFile:
		return fmt.Sprintf("write %s (%d bytes)", c.Path, len(c.Body))
	case ToolEditFile:
		return fmt.Sprintf("edit_file %s (exact replacement)", c.Path)
	case ToolReadFile:
		if start, count, ranged := CanonicalReadRange(c.Offset, c.Limit); ranged {
			return fmt.Sprintf("%s %s (lines %d-%d)", c.Tool, c.Path, start, start+count-1)
		}
		return c.Tool + " " + c.Path
	case ToolWebSearch:
		return fmt.Sprintf("web_search(%q)", strings.TrimSpace(c.Body))
	case ToolWebFetch:
		return "fetch " + c.Path
	case ToolGlob:
		return fmt.Sprintf("glob %q in %s", strings.TrimSpace(c.Body), orWorkspace(c.Path))
	case ToolGrep:
		return fmt.Sprintf("grep %q in %s", strings.TrimSpace(c.Body), orWorkspace(c.Path))
	case ToolPersonalApps:
		return describePersonalAppsCall(c)
	default:
		if c.Path == "" {
			return c.Tool
		}
		return c.Tool + " " + c.Path
	}
}

func orWorkspace(path string) string {
	if path = strings.TrimSpace(path); path != "" {
		return path
	}
	return "."
}

// Instructions is appended to the system prompt while tools are enabled;
// withWeb adds the web tools when the user has turned them on.
func Instructions(root string, withWeb bool) string {
	webTools, webRules := "", ""
	discoveryRoute := "before run_command"
	if withWeb {
		webTools = webFencedForms + "\n"
		webRules = "\n\n" + webInstructions
		discoveryRoute = "before web_search or run_command"
	}
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using tools.
To use a tool, emit a fenced code block whose info string is "tool <name> [path]". Available tools:

- list_dir [path] — list a directory (path optional, defaults to the project root)
- read_file <path> — return a file's contents; an optional JSON body {"offset":1,"limit":200} returns just that 1-based line range
- glob [path] — recursively find files; the glob pattern is the block's body
- grep [path] — recursively search file contents with a regular expression in the block's body
- write_file <path> — create or overwrite a file with the block's body
- edit_file <path> — replace one exact text fragment in an existing file; the block body is one JSON object {"old_text":"…","new_text":"…"}
- run_command — run one shell command in the project directory; the command is the block's body
- ask_user — ask one necessary human question; the block body is one JSON object with question, optional choices (maximum 4), and optional allow_text
- local_context — read bounded local time, system, workspace, process, clipboard, or recent-file facts; the block body is one JSON object with kind (time, system, workspace, processes, clipboard, recent_files) and optional limit. Use kind=time for the current date, time, timezone, weekday, or relative dates (today, tomorrow, next Monday) instead of guessing; clipboard requires human approval
- tool_search — search currently available but hidden tools; the block body is one JSON object with query and optional max_results
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
- glob and grep are read-only and skip .git; recursive grep also skips likely secret files.
- Use ranged read_file when you only need part of a large file. Use edit_file for a small change to an existing file — old_text must match exactly once, so include enough surrounding lines to make it unique. Use write_file only to create a file or deliberately replace all of it.
- run_command takes exactly one command line; save multi-line scripts with write_file first.
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
`+askUserInstructions+`
- Connected MCP schemas may be hidden to save context. The compact MCP directory is authoritative for inventory; use tool_search to make a matching tool callable.
- For an MCP/external-service action whose schema is not already provided, use tool_search %s. Never pass an MCP tool name to run_command. A truncated search result is not the complete catalog.
- When the compact directory gives you a likely tool name, search that name with max_results 1 to avoid loading unrelated schemas. Discovery grants no permission.
- After you emit tool blocks, stop and wait: the results come back in the next user message, marked "%s".
- Use one block per action. If a body contains triple backticks, open the tool block with four.
- When the task is complete, reply normally without any tool blocks.%s`, root, webTools, discoveryRoute, ResultsPrefix, webRules))
}

// ErrDenied is the result error for calls the user rejected.
var ErrDenied = errors.New("denied by the user")

// DeniedResults builds the results message for a rejected batch.
func DeniedResults(calls []Call) []Result {
	out := make([]Result, len(calls))
	for i, c := range calls {
		// The call never reached a producer, so its outcome is genuinely
		// unknown (never OutcomeFailed — the tool itself neither ran nor
		// failed) — combined with agent.ActionDenied at the controller layer,
		// which is what actually records "the user denied this."
		out[i] = Result{Call: c, Err: ErrDenied, Meta: ResultMeta{
			Outcome: OutcomeUnknown,
			Effect:  EffectUnknown,
			Error:   &ErrorInfo{Code: "permission_denied", Retry: RetryNone, Message: boundErrorMessage(ErrDenied)},
		}}
	}
	return out
}
