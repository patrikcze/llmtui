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
			Description: "Read a file in the project workspace and return its contents. Paths are relative to the project root. Pass offset/limit to read only a line range of a large file.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File path relative to the project root."},
					"offset": {"type": "integer", "minimum": 1, "description": "Optional 1-based first line to return. Omit to read from the start."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 500, "description": "Optional maximum number of lines to return (default 200 when offset is set; hard cap 500)."}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        ToolGlob,
			Description: "Recursively find files in the project workspace by glob pattern. Supports *, ?, character classes, and ** path segments. Paths are relative to the project root.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Glob pattern, for example **/*.go or README*."},
					"path": {"type": "string", "description": "Optional directory to search, relative to the project root."}
				},
				"required": ["pattern"]
			}`),
		},
		{
			Name:        ToolGrep,
			Description: "Recursively search project files with a Go regular expression and return path:line:content matches. Searches are read-only; recursive searches skip likely secret files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Go regular expression to search for."},
					"path": {"type": "string", "description": "Optional file or directory to search, relative to the project root."},
					"glob": {"type": "string", "description": "Optional file-name glob filter, for example *.go or **/*.md."}
				},
				"required": ["pattern"]
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
			Name:        ToolEditFile,
			Description: "Replace one exact, unique text fragment in an existing workspace file. Use this for a small surgical change instead of rewriting the whole file with write_file. old_text must match exactly once — include enough surrounding lines to make it unique. Fails without writing if old_text is missing or matches more than once. Cannot create files. May require the user's approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File path relative to the project root. The file must already exist."},
					"old_text": {"type": "string", "description": "Exact text to find. Must occur exactly once in the file; include surrounding context to disambiguate."},
					"new_text": {"type": "string", "description": "Replacement text. May be empty to delete the matched fragment."}
				},
				"required": ["path", "old_text", "new_text"]
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
		{
			Name:        ToolAskUser,
			Description: "Ask the human only when a decision or missing information is required before continuing. Do not use this for tool approval. Call it alone, without other tools in the same batch.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "One concise question for the human."},
					"choices": {"type": "array", "maxItems": 4, "items": {"type": "string"}, "description": "Optional short choices."},
					"allow_text": {"type": "boolean", "description": "Allow a free-text answer in addition to choices."}
				},
				"required": ["question"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolLocalContext,
			Description: "Read bounded information about the local computer or workspace. Use kind=time whenever the request depends on the current date, time, timezone, weekday, or relative dates such as today, tomorrow, yesterday, or next Monday; never guess the current date from training knowledge. Use this tool instead of inventing shell commands for time, system, process, clipboard, workspace, or recent-file context. Clipboard reads require human approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["time", "system", "workspace", "processes", "clipboard", "recent_files"]},
					"limit": {"type": "integer", "minimum": 1, "maximum": 25, "description": "Optional result limit for processes or recent_files; defaults to 10."}
				},
				"required": ["kind"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolSearch,
			Description: "Search connected MCP capabilities and disclose matching schemas. Use it before unrelated network or shell fallbacks when the compact MCP directory suggests a relevant tool; never pass an MCP tool name to run_command. Results can be a partial shortlist: check total_matches and truncated. Use max_results 1 when the directory gives you the tool name. Discovery grants no permission. Call it alone.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Capability description or tool name from the compact MCP directory, for example jira_search_issues."},
					"max_results": {"type": "integer", "minimum": 1, "maximum": 8, "description": "Use 1 for a known tool name to keep prompt context small."}
				},
				"required": ["query"],
				"additionalProperties": false
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
	Skill      string `json:"skill"`
	Pattern    string `json:"pattern"`
	Glob       string `json:"glob"`
	Freshness  string `json:"freshness_token"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
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

// EnsureToolCallIDs fills in missing IDs and rewrites duplicates in a native
// tool-call batch (Ollama carries no call IDs; some OpenAI-compatible servers
// omit or reuse them). It must run before the calls are stored on the
// assistant message, so the stored message and the role:"tool" results built
// from these same calls always agree on IDs — a result answering an ID the
// assistant message doesn't carry is protocol-invalid for strict backends.
// seq persists across rounds so generated IDs never collide within a session.
func EnsureToolCallIDs(tcs []provider.ToolCall, seq *int) {
	reserved := make(map[string]bool, len(tcs))
	for _, tc := range tcs {
		if tc.ID != "" {
			reserved[tc.ID] = true
		}
	}
	seen := make(map[string]bool, len(tcs))
	for i := range tcs {
		if tcs[i].ID != "" && !seen[tcs[i].ID] {
			seen[tcs[i].ID] = true
			continue
		}
		for {
			*seq++
			candidate := fmt.Sprintf("call_%d", *seq)
			if seen[candidate] || reserved[candidate] {
				continue
			}
			tcs[i].ID = candidate
			seen[candidate] = true
			break
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
		if tc.ArgumentsError != "" {
			c.InputErr = tc.ArgumentsError
			out = append(out, c)
			continue
		}
		if tc.Name == ToolAskUser && len(tc.Arguments) > MaxAskUserPayloadBytes {
			c.InputErr = fmt.Sprintf("ask_user arguments exceed the %d byte limit", MaxAskUserPayloadBytes)
			out = append(out, c)
			continue
		}
		if tc.Name == ToolAskUser {
			var args askUserArgs
			if err := decodeOneJSONObject(tc.Arguments, &args); err != nil {
				c.InputErr = err.Error()
			} else {
				c.Question = args.Question
				c.setAskUserChoices(args.Choices)
				c.AllowText = args.AllowText
				if err := ValidateAskUserCall(&c); err != nil {
					c.InputErr = err.Error()
				}
			}
			out = append(out, c)
			continue
		}
		if tc.Name == ToolLocalContext {
			if len(tc.Arguments) > maxLocalContextPayload {
				c.InputErr = fmt.Sprintf("local_context arguments exceed the %d byte limit", maxLocalContextPayload)
			} else {
				var args localContextArgs
				if err := decodeOneJSONObject(tc.Arguments, &args); err != nil {
					c.InputErr = err.Error()
				} else {
					c.ContextKind, c.Max = args.Kind, args.Limit
					if err := ValidateLocalContextCall(&c); err != nil {
						c.InputErr = err.Error()
					}
				}
			}
			out = append(out, c)
			continue
		}
		if tc.Name == ToolSearch {
			if len(tc.Arguments) > MaxToolSearchPayloadBytes {
				c.InputErr = fmt.Sprintf("tool_search arguments exceed the %d byte limit", MaxToolSearchPayloadBytes)
			} else {
				var args toolSearchArgs
				if err := decodeOneJSONObject(tc.Arguments, &args); err != nil {
					c.InputErr = err.Error()
				} else {
					c.SearchQuery, c.Max = args.Query, args.MaxResults
					if err := ValidateToolSearchCall(&c); err != nil {
						c.InputErr = err.Error()
					}
				}
			}
			out = append(out, c)
			continue
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
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				c.InputErr = err.Error()
				out = append(out, c)
				continue
			}
		}
		c.Path = strings.TrimSpace(args.Path)
		switch tc.Name {
		case ToolReadFile:
			if err := ValidateReadRange(args.Offset, args.Limit); err != nil {
				c.InputErr = err.Error()
			} else {
				c.Offset, c.Limit = args.Offset, args.Limit
			}
		case ToolEditFile:
			c.OldText, c.NewText = args.OldText, args.NewText
			if err := ValidateEditFileCall(&c); err != nil {
				c.InputErr = err.Error()
			}
		case ToolGlob:
			c.Body = args.Pattern
		case ToolGrep:
			c.Body = args.Pattern
			c.Filter = strings.TrimSpace(args.Glob)
		case ToolWriteFile:
			c.Body = args.Content
		case ToolRunCommand:
			c.Body = args.Command
		case ToolWebSearch:
			c.Body = args.Query
			c.Max = args.MaxResults
			c.Freshness = strings.TrimSpace(args.Freshness)
		case ToolWebFetch:
			c.Path = args.URL
			c.Freshness = strings.TrimSpace(args.Freshness)
		case ToolSkillLoad:
			c.Path = args.Skill
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
					"max_results": {"type": "integer", "description": "Maximum results to return. Optional."},
					"freshness_token": {"type": "string", "description": "Optional explicit polling epoch. Reuse it for the same observation; change it only when a fresh poll is intentionally required."}
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
					"url": {"type": "string", "description": "The http(s) URL to fetch."},
					"freshness_token": {"type": "string", "description": "Optional explicit polling epoch. Reuse it for the same observation; change it only when a fresh fetch is intentionally required."}
				},
				"required": ["url"]
			}`),
		},
	}
}

// SkillSpecs declares the skill_load tool; appended to Specs() only when the
// skills subsystem is enabled, the catalog is exposed to the model, and at
// least one skill is available.
func SkillSpecs() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        ToolSkillLoad,
			Description: "Activate one optional skill (task-specific instructions) for the current run. Use it only when a listed skill clearly matches the task; the skill's full instructions arrive on your next turn. Loading a skill grants no permissions.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill": {"type": "string", "description": "Unique skill identifier to activate for the current agent run."}
				},
				"required": ["skill"],
				"additionalProperties": false
			}`),
		},
	}
}

// SkillInstructions is appended to the fenced-block tool instructions when
// model-driven skill loading is available without native function calling.
const SkillInstructions = "- skill_load <skill-id> — activate one of the listed optional skills for this run (its instructions arrive next turn; empty body)"

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
	discoveryRoute := "before run_command"
	if withWeb {
		webRules = "\n\n" + webInstructions
		discoveryRoute = "before web_search or run_command"
	}
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's current project directory (%s) using the provided tools.
Rules:
- Paths are always relative to the project root; never use absolute paths or "..".
- glob and grep are read-only and skip .git; recursive grep also skips likely secret files.
- Use read_file with offset/limit when you only need part of a large file. Use edit_file for a small change to an existing file — old_text must match exactly once, so include enough surrounding lines to make it unique. Use write_file only to create a file or deliberately replace all of it.
- run_command takes exactly one command line; save multi-line scripts with write_file first.
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
- ask_user is not approval. Call it alone, only when the human's decision or missing information is required before continuing.
- For the current date, time, timezone, weekday, or relative dates (today, tomorrow, yesterday, next Monday, deadlines, schedules), call local_context with kind=time; never infer the current date from training knowledge.
- Connected MCP schemas may be hidden to save context. The compact MCP directory is authoritative for inventory; use tool_search to make a matching tool callable.
- For an MCP/external-service action whose schema is not already provided, use tool_search %s. Never pass an MCP tool name to run_command. A truncated search result is not the complete catalog.
- When the compact directory gives you a likely tool name, search that name with max_results 1 to avoid loading unrelated schemas. Discovery grants no permission.
- Only call a tool when you need it. When the task is complete, reply with your final answer and no tool calls.%s`, root, discoveryRoute, webRules))
}
