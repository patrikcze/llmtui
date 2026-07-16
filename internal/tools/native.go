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
	Path       string `json:"path"`
	Content    string `json:"content"`
	Command    string `json:"command"`
	Query      string `json:"query"`
	URL        string `json:"url"`
	MaxResults int    `json:"max_results"`
}

// mcpToolPrefix marks a native tool name as routing to an MCP server's tool:
// "mcp__<server>__<tool>". internal/tui builds names in this shape when
// assembling tool specs for a connected server; SplitMCPToolName splits them
// back out on the way in.
const mcpToolPrefix = "mcp__"

// JoinMCPToolName builds the native tool name that exposes one MCP server's
// tool to the model, matching SplitMCPToolName.
func JoinMCPToolName(server, tool string) string {
	return mcpToolPrefix + server + "__" + tool
}

// SplitMCPToolName splits a native tool name of the form
// "mcp__<server>__<tool>" into its server and tool parts. ok is false if the
// name doesn't have the prefix, or either part would be empty — the caller
// falls back to treating it as an ordinary tool name.
func SplitMCPToolName(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, mcpToolPrefix)
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, "__")
	if !found || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}

// EnsureToolCallIDs fills in IDs for native tool calls whose backend supplied
// none (Ollama's native API carries no call IDs; some OpenAI-compatible
// servers omit them). It must run before the calls are stored on the
// assistant message, so the stored message and the role:"tool" results built
// from these same calls always agree on IDs — a result answering an ID the
// assistant message doesn't carry is protocol-invalid for strict backends.
// seq persists across rounds so generated IDs never collide within a session.
func EnsureToolCallIDs(tcs []provider.ToolCall, seq *int) {
	for i := range tcs {
		if tcs[i].ID == "" {
			*seq++
			tcs[i].ID = fmt.Sprintf("call_%d", *seq)
		}
	}
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
		if server, tool, ok := SplitMCPToolName(tc.Name); ok {
			c.MCPServer, c.MCPTool = server, tool
			c.MCPArgs = tc.Arguments
			if strings.TrimSpace(c.MCPArgs) == "" {
				c.MCPArgs = "{}"
			}
			out = append(out, c)
			continue
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
		case ToolWebSearch:
			c.Body = args.Query
			c.Max = args.MaxResults
		case ToolWebFetch:
			c.Path = args.URL
		}
		out = append(out, c)
	}
	return out
}

// WebSpecs declares the web tools; appended to Specs() only when the user
// has enabled web access.
func WebSpecs() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        ToolWebSearch,
			Description: "Search the web (DuckDuckGo) and get result titles, URLs, and snippets. Use it to find current information, then web_fetch the most promising URL.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "The search query."},
					"max_results": {"type": "integer", "description": "Maximum results to return. Optional."}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        ToolWebFetch,
			Description: "Fetch one web page and return its readable content as Markdown. May require the user's approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "The http(s) URL to fetch."}
				},
				"required": ["url"]
			}`),
		},
	}
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
// withWeb adds the web-tool rules when the user has turned them on.
func NativeInstructions(root string, withWeb bool) string {
	webRules := ""
	if withWeb {
		webRules = "\n\n" + webInstructions
	}
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using the provided tools.
Rules:
- Paths are always relative to the project root; never use absolute paths or "..".
- run_command takes exactly one command line; save multi-line scripts with write_file first.
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
- Only call a tool when you need it. When the task is complete, reply with your final answer and no tool calls.%s`, root, webRules))
}
