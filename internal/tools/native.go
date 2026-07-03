package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Specs declares the workspace tools in the standard function-calling format,
// for backends with native tool support (Ollama tools, OpenAI-compatible
// servers). Models trained for tool use follow this protocol far more
// reliably than the fenced-block fallback.
func Specs() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        ToolListDir,
			Description: "List a directory in the project workspace. Paths are relative to the project root; omit path for the root itself.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Directory path relative to the project root. Optional; defaults to the root."}
				}
			}`),
		},
		{
			Name:        ToolReadFile,
			Description: "Read a file in the project workspace and return its contents. Paths are relative to the project root.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File path relative to the project root."}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        ToolWriteFile,
			Description: "Create or overwrite a file in the project workspace with the given content. Paths are relative to the project root. May require the user's approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File path relative to the project root."},
					"content": {"type": "string", "description": "The full file content to write."}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        ToolRunCommand,
			Description: "Run one shell command in the project workspace and return its output. Exactly one command line; save multi-line scripts with write_file first. Non-read-only commands may require the user's approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The command line to execute."}
				},
				"required": ["command"]
			}`),
		},
	}
}

// nativeArgs is the union of all tool argument schemas.
type nativeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Command string `json:"command"`
}

// CallsFromNative converts native function calls into runnable Calls.
// Malformed arguments still produce a Call so the runner can report the
// problem back to the model instead of the batch silently vanishing. Missing
// IDs are filled in so results can always be correlated.
func CallsFromNative(tcs []provider.ToolCall) []Call {
	out := make([]Call, 0, len(tcs))
	for i, tc := range tcs {
		c := Call{ID: tc.ID, Tool: tc.Name}
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		var args nativeArgs
		if strings.TrimSpace(tc.Arguments) != "" {
			// A decode error leaves the fields empty; Execute reports what is
			// missing (e.g. "read_file needs a path") and the model retries.
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		c.Path = args.Path
		switch tc.Name {
		case ToolWriteFile:
			c.Body = args.Content
		case ToolRunCommand:
			c.Body = args.Command
		}
		out = append(out, c)
	}
	return out
}

// NativeResults renders execution results as role:"tool" messages, one per
// call, per the standard function-calling protocol.
func NativeResults(results []Result) []provider.Message {
	out := make([]provider.Message, 0, len(results))
	for _, res := range results {
		content := res.Output
		if res.Err != nil {
			content = "error: " + res.Err.Error()
			if res.Output != "" {
				content += "\n" + res.Output
			}
		}
		out = append(out, provider.Message{
			Role:       provider.RoleTool,
			Content:    content,
			ToolCallID: res.Call.ID,
			ToolName:   res.Call.Tool,
			Display:    res.Diff,
		})
	}
	return out
}

// LimitResults builds the results for a batch that was not executed because
// the per-turn iteration budget ran out. Instead of dead-ending the turn, it
// tells the model to wrap up, so the user still gets a final answer.
func LimitResults(calls []Call, max int) []Result {
	err := fmt.Errorf("tool iteration limit reached (%d rounds this turn, tools.max_iterations) — this call was not executed. Do not request more tools; give your final answer now using what you already know", max)
	out := make([]Result, len(calls))
	for i, c := range calls {
		out[i] = Result{Call: c, Err: err}
	}
	return out
}

// NativeInstructions is appended to the system prompt when tools are offered
// natively; the protocol itself needs no explanation, only the house rules.
func NativeInstructions(root string) string {
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using the provided tools.
Rules:
- Paths are always relative to the project root; never use absolute paths or "..".
- run_command takes exactly one command line; save multi-line scripts with write_file first.
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
- Only call a tool when you need it. When the task is complete, reply with your final answer and no tool calls.`, root))
}
