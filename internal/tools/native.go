package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/personalapps"
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
			Description: "List one directory level. For a recursive view use directory_tree; to find files by name use glob.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Directory relative to the project root; omit for the root."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 200},
					"cursor": {"type": "string", "description": "The cursor a previous page returned."}
				},
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolReadFile,
			Description: "Read a text file. Without offset/limit it returns a bounded first window and says how to continue. A result's resource_id identifies that exact snapshot: pass it as expected_resource_id to edit_file/write_file, or as resource_id here to page a retained tool output without rerunning it.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File relative to the project root. Omit when passing resource_id."},
					"offset": {"type": "integer", "minimum": 1, "description": "1-based first line."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 500, "description": "Lines to return (default 200 with offset)."},
					"byte_offset": {"type": "integer", "minimum": 0, "description": "Continue a partial giant line from next_byte_offset; not with offset/limit."},
					"resource_id": {"type": "string", "description": "ent_... ID of a retained output to page instead of a path."}
				},
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolReadMultipleFiles,
			Description: "Read up to 10 files in one call, each with read_file's default window under a ==> path <== header. A failing path is reported inline. Use read_file for line ranges or a version to edit against.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"paths": {"type": "array", "minItems": 1, "maxItems": 10, "items": {"type": "string"}, "description": "Files relative to the project root."}
				},
				"required": ["paths"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolGetFileInfo,
			Description: "Metadata of one file or directory (type, size, permissions, timestamps) without reading it. Symlinks are described, not followed.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path relative to the project root; omit for the root."}
				},
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolDirectoryTree,
			Description: "Recursive JSON tree of a directory (name, type, children). Entries cut off by depth or size carry truncated=true. Skips .git; never follows symlinks.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Directory relative to the project root; omit for the root."},
					"depth": {"type": "integer", "minimum": 1, "maximum": 10, "default": 3},
					"exclude": {"type": "array", "maxItems": 16, "items": {"type": "string"}, "description": "Glob patterns to skip, e.g. node_modules or **/*.log."}
				},
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolGlob,
			Description: "Find files by name pattern, recursively. Supports *, ?, [classes] and ** segments; a pattern without / matches names at any depth, so *config* finds every file whose name contains config. Skips .git.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "For example **/*.go, README*, or *test*."},
					"path": {"type": "string", "description": "Directory to search, relative to the project root; omit for the root."},
					"exclude": {"type": "array", "maxItems": 16, "items": {"type": "string"}, "description": "Glob patterns to skip; an excluded directory is not descended into."},
					"case_sensitive": {"type": "boolean", "default": true},
					"limit": {"type": "integer", "minimum": 1, "maximum": 200},
					"cursor": {"type": "string", "description": "The cursor a previous page returned."}
				},
				"required": ["pattern"]
			}`),
		},
		{
			Name:        ToolGrep,
			Description: "Search file contents, recursively, and return matching lines with paths and line numbers. Read-only; skips .git and likely secret files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Go regular expression, or plain text with literal=true."},
					"path": {"type": "string", "description": "File or directory to search, relative to the project root; omit for the root."},
					"resource_id": {"type": "string", "description": "Search a retained tool output instead of workspace files."},
					"glob": {"type": "string", "description": "File-name filter, e.g. *.go."},
					"literal": {"type": "boolean", "default": false},
					"case_sensitive": {"type": "boolean", "default": true},
					"context": {"type": "integer", "minimum": 0, "maximum": 5, "default": 0, "description": "Lines of context around each match."},
					"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 100},
					"cursor": {"type": "string", "description": "The cursor a previous page returned; send it without the other search fields."}
				},
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolWriteFile,
			Description: "Create a file, or replace a whole file, with the given content. For a change to an existing file prefer edit_file.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "File relative to the project root."},
					"content": {"type": "string", "description": "The full file content."},
					"expected_resource_id": {"type": "string", "description": "resource_id of a complete earlier read; the write is rejected if the file changed since."}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        ToolEditFile,
			Description: "Replace exact text in an existing file. Give old_text/new_text, or edits applied in order, all or nothing. Each old_text must occur exactly once — include enough surrounding lines to make it unique; nothing is written if any fails. Cannot create files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Existing file relative to the project root."},
					"old_text": {"type": "string", "description": "Exact text to replace. Omit when using edits."},
					"new_text": {"type": "string", "description": "Replacement; empty deletes old_text. Omit when using edits."},
					"edits": {"type": "array", "minItems": 1, "maxItems": 16, "items": {"type": "object", "properties": {"old_text": {"type": "string"}, "new_text": {"type": "string"}}, "required": ["old_text", "new_text"], "additionalProperties": false}, "description": "Several replacements, each applied to the previous result."},
					"dry_run": {"type": "boolean", "default": false, "description": "Return the unified diff without writing."},
					"expected_resource_id": {"type": "string", "description": "resource_id of a complete earlier read; the edit is rejected if the file changed since."}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        ToolCreateDirectory,
			Description: "Create a directory and any missing parents. An existing directory is fine.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Directory relative to the project root."}
				},
				"required": ["path"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolMoveFile,
			Description: "Move or rename a file or directory; missing destination parents are created. Never overwrites: fails if the destination exists. Symlinks are not moved.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"source": {"type": "string", "description": "Existing path relative to the project root."},
					"destination": {"type": "string", "description": "New path relative to the project root; must not exist."}
				},
				"required": ["source", "destination"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolRunCommand,
			Description: "Run one shell command line in the project directory and return its output. For builds, tests, git, and other programs; use the file tools to read, list, search, or edit files.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Exactly one command line."}
				},
				"required": ["command"]
			}`),
		},
		{
			Name:        ToolAskUser,
			Description: "Ask the user one necessary question and wait for the answer, instead of asking in reply text. Up to 4 choices show a picker; no choices shows text input. Do not use this for tool approval. Call it alone.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "One concise question."},
					"choices": {"type": "array", "maxItems": 4, "items": {"type": "string"}, "description": "Optional short choices."},
					"allow_text": {"type": "boolean", "description": "Also allow a free-text answer."}
				},
				"required": ["question"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolLocalContext,
			Description: "Read bounded facts about this computer. Use kind=time whenever the answer depends on the current date, time, timezone, weekday, or relative dates such as today, tomorrow, or next Monday; never guess them from training knowledge. Use it instead of shell commands for time, system, processes, clipboard, workspace, or recent files. Clipboard reads require human approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["time", "system", "workspace", "processes", "clipboard", "recent_files"]},
					"limit": {"type": "integer", "minimum": 1, "maximum": 25, "description": "Result limit for processes or recent_files; default 10."}
				},
				"required": ["kind"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolSearch,
			Description: "Search the connected MCP tools hidden from this list and make matches callable. Use it when the MCP directory suggests a tool you do not have yet; never pass an MCP tool name to run_command. Results may be a partial shortlist (see total_matches, truncated). Discovery grants no permission. Call it alone.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Tool name from the MCP directory, e.g. jira_search_issues, or a capability description."},
					"max_results": {"type": "integer", "minimum": 1, "maximum": 8, "description": "Use 1 for a known tool name."}
				},
				"required": ["query"],
				"additionalProperties": false
			}`),
		},
		{
			Name:        ToolGetEntityDetails,
			Description: "Look up data stored earlier this session (files read, web pages, tool results, image observations). Use query to find up to 8 minimal candidates, then entity_ids with level=full to expand exact returned IDs; one selector per call. For an earlier image use kinds=[vision_observation]; a web result is not image evidence. Read-only; call it alone.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "minLength": 1, "maxLength": 512, "description": "Name, path, URL, or topic keywords."},
					"entity_ids": {"type": "array", "items": {"type": "string", "pattern": "^ent_[0-9]{5}$"}, "minItems": 1, "maxItems": 8},
					"level": {"type": "string", "enum": ["identifier", "minimal", "full"]},
					"kinds": {"type": "array", "items": {"type": "string", "enum": ["web_result", "web_page", "file", "mcp_result", "collection", "vision_observation"]}, "minItems": 1, "maxItems": 6}
				},
				"oneOf": [{"required": ["entity_ids"]}, {"required": ["query"]}],
				"additionalProperties": false
			}`),
		},
	}
}

// nativeArgs is the union of all tool argument schemas.
type nativeArgs struct {
	Path               string     `json:"path"`
	Content            string     `json:"content"`
	Command            string     `json:"command"`
	Query              string     `json:"query"`
	URL                string     `json:"url"`
	MaxResults         int        `json:"max_results"`
	Skill              string     `json:"skill"`
	Pattern            string     `json:"pattern"`
	Glob               string     `json:"glob"`
	Freshness          string     `json:"freshness_token"`
	Offset             int        `json:"offset"`
	Limit              int        `json:"limit"`
	Literal            bool       `json:"literal"`
	CaseSensitive      *bool      `json:"case_sensitive"`
	Context            int        `json:"context"`
	Cursor             string     `json:"cursor"`
	ByteOffset         *int64     `json:"byte_offset"`
	ResourceID         string     `json:"resource_id"`
	OldText            string     `json:"old_text"`
	NewText            string     `json:"new_text"`
	ExpectedResourceID string     `json:"expected_resource_id"`
	CacheMode          string     `json:"cache_mode"`
	CacheMaxAge        int        `json:"cache_max_age"`
	RefreshEpoch       string     `json:"refresh_epoch"`
	EntityIDs          []string   `json:"entity_ids"`
	Level              string     `json:"level"`
	Kinds              []string   `json:"kinds"`
	Exclude            []string   `json:"exclude"`
	Paths              []string   `json:"paths"`
	Edits              []EditPair `json:"edits"`
	DryRun             bool       `json:"dry_run"`
	Source             string     `json:"source"`
	Destination        string     `json:"destination"`
	Depth              int        `json:"depth"`
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
		if tc.Name == ToolSequentialThinking {
			// Dedicated decoder: it accepts the reference server's camelCase
			// spellings as well as llmtui's snake_case schema.
			decodeThinkingArgs(&c, tc.Arguments)
			out = append(out, c)
			continue
		}
		if tc.Name == ToolWebResearch {
			// web_research has its own tolerant decoder (queries may be a
			// single string) shared with the fenced JSON form.
			decodeWebResearchArgs(&c, tc.Arguments)
			out = append(out, c)
			continue
		}
		if tc.Name == ToolGetEntityDetails {
			if len(tc.Arguments) > MaxEntityDetailsPayloadBytes {
				c.InputErr = fmt.Sprintf("get_entity_details arguments exceed the %d byte limit", MaxEntityDetailsPayloadBytes)
			} else {
				var args entityDetailsArgs
				if err := decodeOneJSONObject(tc.Arguments, &args); err != nil {
					c.InputErr = err.Error()
				} else {
					setEntityIDs(&c, args.EntityIDs)
					c.EntityLevel = args.Level
					c.SearchQuery = args.Query
					setEntityKinds(&c, args.Kinds)
					if err := ValidateEntityDetailsCall(&c); err != nil {
						c.InputErr = err.Error()
					}
				}
			}
			out = append(out, c)
			continue
		}
		if op := personalapps.Operation(tc.Name); op.Valid() {
			// Native personal_apps tools are exposed one-per-operation with a
			// flat schema (PersonalAppsSpecs) so a model calls them like every
			// other native tool. internal/personalapps.ParseRequest still
			// requires the combined {"operation","arguments"} envelope, so
			// that reshaping happens here, once, built from data this file
			// fully controls — the operation name the model dispatched
			// through — never inferred from the model's own argument shape.
			// That is the difference from a tolerant decoder that reshapes
			// whatever top-level fields a call happens to send: this can't
			// misattribute a field, because it never looks at tc.Arguments'
			// keys to decide anything, only splices them in verbatim as
			// "arguments" for ParseRequest's own decoder to validate.
			c.Tool = ToolPersonalApps
			if len(tc.Arguments) > MaxPersonalAppsPayloadBytes {
				c.InputErr = fmt.Sprintf("%s arguments exceed the %d byte limit", tc.Name, MaxPersonalAppsPayloadBytes)
			} else if env, err := personalAppsEnvelope(op, tc.Arguments); err != nil {
				c.InputErr = fmt.Sprintf("%s arguments are not valid JSON: %v", tc.Name, err)
			} else {
				c.Body = env
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
		args, notes, err := decodeNativeArgs(tc.Name, tc.Arguments)
		if err != nil {
			c.InputErr = err.Error()
			out = append(out, c)
			continue
		}
		c.ArgumentNotes = strings.Join(notes, ",")
		c.Path = strings.TrimSpace(args.Path)
		switch tc.Name {
		case ToolListDir:
			c.SearchLimit, c.SearchCursor = args.Limit, strings.TrimSpace(args.Cursor)
		case ToolReadFile:
			c.ResourceID = strings.TrimSpace(args.ResourceID)
			if err := ValidateReadSelectors(c.Path, c.ResourceID); err != nil {
				c.InputErr = err.Error()
			} else if err := ValidateReadArguments(args.Offset, args.Limit, args.ByteOffset); err != nil {
				c.InputErr = err.Error()
			} else {
				c.Offset, c.Limit = args.Offset, args.Limit
				c.ByteOffset = args.ByteOffset
			}
		case ToolEditFile:
			c.DryRun = args.DryRun
			c.ExpectedResourceID = strings.TrimSpace(args.ExpectedResourceID)
			if err := setEditPairs(&c, args.OldText, args.NewText, args.Edits); err != nil {
				c.InputErr = err.Error()
			} else if err := ValidateEditFileCall(&c); err != nil {
				c.InputErr = err.Error()
			}
		case ToolReadMultipleFiles:
			if err := setReadPaths(&c, args.Paths); err != nil {
				c.InputErr = err.Error()
			}
		case ToolDirectoryTree:
			setDirectoryTreeOptions(&c, args.Depth, args.Exclude)
		case ToolMoveFile:
			c.Path, c.Destination = strings.TrimSpace(args.Source), strings.TrimSpace(args.Destination)
			if err := ValidateMoveFileCall(&c); err != nil {
				c.InputErr = err.Error()
			}
		case ToolGlob:
			c.Body = args.Pattern
			c.SearchCaseSensitive = args.CaseSensitive
			c.SearchLimit, c.SearchCursor = args.Limit, strings.TrimSpace(args.Cursor)
			if err := setGlobExcludes(&c, args.Exclude); err != nil {
				c.InputErr = err.Error()
			}
		case ToolGrep:
			c.Body = args.Pattern
			c.Filter = strings.TrimSpace(args.Glob)
			c.ResourceID = strings.TrimSpace(args.ResourceID)
			c.SearchLiteral, c.SearchCaseSensitive = args.Literal, args.CaseSensitive
			c.SearchContext, c.SearchLimit = args.Context, args.Limit
			c.SearchCursor = strings.TrimSpace(args.Cursor)
			if err := ValidateSearchCall(&c); err != nil {
				c.InputErr = err.Error()
			}
		case ToolWriteFile:
			c.Body = args.Content
			c.ExpectedResourceID = strings.TrimSpace(args.ExpectedResourceID)
			if err := ValidateWriteFileCall(&c); err != nil {
				c.InputErr = err.Error()
			}
		case ToolRunCommand:
			c.Body = args.Command
		case ToolWebSearch:
			c.Body = args.Query
			c.Max = args.MaxResults
			c.Freshness = strings.TrimSpace(args.Freshness)
		case ToolWebFetch:
			c.Path = args.URL
			c.Freshness = strings.TrimSpace(args.Freshness)
			c.WebCacheMode = strings.TrimSpace(args.CacheMode)
			c.WebCacheMaxAge = args.CacheMaxAge
			c.WebRefreshEpoch = strings.TrimSpace(args.RefreshEpoch)
		case ToolSkillLoad:
			c.Path = args.Skill
		}
		out = append(out, c)
	}
	return out
}

// personalAppsEnvelope builds the {"operation","arguments"} wire form
// internal/personalapps.ParseRequest requires, from one native call's
// operation name and raw arguments. args is spliced in as a json.RawMessage
// — copied byte-for-byte into the result, never parsed and re-serialized —
// so ParseRequest's own decoder remains the sole authority on the request's
// well-formedness, duplicate keys included. Empty/missing arguments become
// "{}", matching how the fenced protocol already treats an absent body.
func personalAppsEnvelope(op personalapps.Operation, args string) (string, error) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		trimmed = "{}"
	}
	raw, err := json.Marshal(struct {
		Operation string          `json:"operation"`
		Arguments json.RawMessage `json:"arguments"`
	}{Operation: string(op), Arguments: json.RawMessage(trimmed)})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// WebSpecs declares the web tools; appended to Specs() only when the user
// has enabled web access.
func WebSpecs() []provider.ToolSpec {
	return []provider.ToolSpec{
		{
			Name:        ToolWebSearch,
			Description: "Search the web (DuckDuckGo) for result titles, URLs, and snippets. Use it to find a specific page, then web_fetch the best URL. To answer a question, prefer web_research.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string"},
					"max_results": {"type": "integer", "description": "Optional result limit."},
					"freshness_token": {"type": "string", "description": "Optional. Change it only to repeat an identical search on purpose."}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        ToolWebFetch,
			Description: "Fetch one http(s) page or JSON/XML endpoint and return its readable content as Markdown. May reuse a snapshot from earlier in this session; cache_mode=refresh forces a new fetch. May require the user's approval.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "The http(s) URL."},
					"freshness_token": {"type": "string", "description": "Optional. Change it only to repeat an identical fetch on purpose."},
					"cache_mode": {"type": "string", "enum": ["auto", "cached", "refresh"], "default": "auto", "description": "auto may reuse a recent snapshot, cached requires one, refresh fetches anew."},
					"cache_max_age": {"type": "integer", "minimum": 0, "maximum": 86400, "description": "Maximum snapshot age in seconds for auto."}
				},
				"required": ["url"]
			}`),
		},
		{
			Name:        ToolWebResearch,
			Description: "Research a question on the web in one call: up to 3 searches, reads the best few pages from different sites, and returns numbered notes with the relevant passages to cite as [1], [2]. Prefer it over web_search + web_fetch for facts, current events, prices, versions, or anything that may have changed. Asks the user's approval once per request.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "The question, in full."},
					"queries": {"type": "array", "items": {"type": "string"}, "maxItems": 3, "description": "1-3 distinct search queries, e.g. other phrasings or the official source. Defaults to the question."},
					"max_sources": {"type": "integer", "minimum": 1, "maximum": 6, "description": "Pages to read; default 4."},
					"freshness_token": {"type": "string", "description": "Optional. Change it only to repeat an identical research request on purpose."}
				},
				"required": ["question"]
			}`),
		},
	}
}

// PersonalAppsSpecs declares one native tool per personal_apps operation,
// appended to Specs() only when the feature is enabled and this build's
// platform can reach it (see internal/personalapps.Service.
// PlatformSupported).
//
// This used to be a single "personal_apps" tool taking a loose
// {"operation":"<op>","arguments":{...}} envelope, on the theory that a
// per-operation schema union is unreliable on local models. That theory was
// about *conditional* schemas — one tool whose parameter shape branches on
// an "operation" field, which is a known-shaky pattern for JSON-schema-to-
// grammar conversion. This is a different, more standard shape: twelve
// separate tools, each with its own flat, unconditional schema, exactly
// like every other tool in this file. Reproduced live with the loose form:
// a model correctly picked the right field name but put it at the call's
// top level instead of nested under "arguments", because every other tool
// in the same catalog is flat — nothing here asked it to nest anything.
// CallsFromNative reassembles internal/personalapps.ParseRequest's required
// {"operation","arguments"} envelope from the operation name a spec was
// called by; see personalAppsEnvelope's doc comment for why that reshaping
// is safe. ParseRequest remains the sole authority on whether a call is
// actually valid for its operation — this schema only narrows what a model
// has to guess before finding that out.
func PersonalAppsSpecs() []provider.ToolSpec {
	return personalAppsSpecsFor(personalapps.Operations())
}

// PersonalAppsSpecsFor returns only the specs a caller may currently invoke,
// given personalapps.StatusView.Operations. Offering the full catalog
// regardless of connection state contradicts Scope.AllowedOperations' own
// contract ("a disabled or disconnected adapter's operations are absent
// rather than present and failing") and was observed live: with Calendar
// enabled but never connected, a model called calendar_list, got
// app_unavailable, and had no way to tell a permanently ungranted adapter
// from a transient outage. Only PersonalAppsSpecs' full catalog feeds the
// capability registry, which must classify every operation name whether or
// not it is callable right now.
func PersonalAppsSpecsFor(allowed []personalapps.Operation) []provider.ToolSpec {
	return personalAppsSpecsFor(allowed)
}

func personalAppsSpecsFor(ops []personalapps.Operation) []provider.ToolSpec {
	specs := make([]provider.ToolSpec, 0, len(ops))
	for _, op := range ops {
		if !op.Valid() {
			continue
		}
		specs = append(specs, provider.ToolSpec{
			Name:        string(op),
			Description: personalAppsOperationDescription(op),
			Parameters:  personalAppsOperationSchema(op),
		})
	}
	return specs
}

// personalAppsOperationDescription gives each personal_apps native tool a
// short, call-specific description. The shared house rules (call status
// first, coverage honesty, the two-step change flow, untrusted content) are
// deliberately not repeated in each of these — they live once in
// PersonalAppsInstructions (personal_apps.go), which every protocol using
// this tool family already receives in the system prompt.
func personalAppsOperationDescription(op personalapps.Operation) string {
	switch op {
	case personalapps.OpStatus:
		return "Report which personal_apps operations and change types are currently permitted. No personal content, no permission prompt. Call this first if you have not already this turn."
	case personalapps.OpMailAccounts:
		return "List in-scope Mail accounts: identity and label only."
	case personalapps.OpMailMailboxes:
		return "List mailbox metadata within one Mail account, or one mailbox's children."
	case personalapps.OpMailSearch:
		return "Bounded metadata search across selected Mail accounts or mailboxes. No message content and no free-form predicate — see mail_read for content."
	case personalapps.OpMailRead:
		return "Read bounded content for explicitly selected Mail messages, by id from a prior mail_search result."
	case personalapps.OpCalendarList:
		return "List in-scope calendars with writability."
	case personalapps.OpCalendarEvents:
		return "List occurrences overlapping a time window, in selected calendars."
	case personalapps.OpCalendarEvent:
		return "Read one selected event, by id from a prior calendar_events result."
	case personalapps.OpCalendarFreeSlots:
		return "Compute deterministic free-time gaps from observed busy intervals in the selected calendars only — never other people's availability."
	case personalapps.OpChangePrepare:
		return "Validate a bounded set of Mail/Calendar changes and return an immutable plan_id preview. Performs no external write. mail_move, mail_set_read, mail_set_flag, mail_save_draft, calendar_create_event, and calendar_update_event are change TYPES for this tool's changes[] entries, never separate tools — do not call one of those names directly; it will fail with \"unknown tool\"."
	case personalapps.OpChangeApply:
		return "Execute one plan a human has already approved in their own review. There is no argument that grants approval yourself."
	case personalapps.OpOpenItem:
		return "Ask the owning app to open one existing Mail/Calendar item. Not a general file or URL opener."
	default:
		return "Optional Apple Mail/Calendar integration operation."
	}
}

// personalAppsFieldSchemas declares the JSON Schema fragment for every field
// any personal_apps operation accepts. personalAppsOperationSchema selects
// the subset one operation actually uses, so each field's type and
// description are written once here rather than duplicated across twelve
// schemas.
func personalAppsFieldSchemas() map[string]map[string]any {
	str := map[string]any{"type": "string"}
	strArray := map[string]any{"type": "array", "items": str}
	boolField := map[string]any{"type": "boolean"}
	intField := map[string]any{"type": "integer"}
	field := func(base map[string]any, desc string) map[string]any {
		out := make(map[string]any, len(base)+1)
		for k, v := range base {
			out[k] = v
		}
		out["description"] = desc
		return out
	}
	idDesc := "an opaque id copied verbatim from a prior result — never invented, never a value from a different field."
	return map[string]map[string]any{
		"limit":                   field(intField, "Page size; defaults to the configured page size."),
		"cursor":                  field(str, "Continues a previous result's next_cursor."),
		"account_id":              field(str, "The account to list, "+idDesc+" (from mail_accounts)."),
		"parent_id":               field(str, "List this mailbox's children instead of the account root, "+idDesc+" (from a prior mail_mailboxes result)."),
		"account_ids":             field(strArray, "Search these accounts' top-level Inbox, ids from mail_accounts. Prefer mailbox_ids when you already have one from mail_mailboxes."),
		"mailbox_ids":             field(strArray, "Search these specific mailboxes, ids from a mail_mailboxes result."),
		"received_after":          field(str, "RFC3339 timestamp, inclusive lower bound on received time."),
		"received_before":         field(str, "RFC3339 timestamp, exclusive upper bound on received time."),
		"from":                    field(str, "Case-insensitive substring match against the sender."),
		"subject":                 field(str, "Case-insensitive substring match against the subject."),
		"unread":                  field(boolField, "Filter to unread (true) or read (false) messages."),
		"flagged":                 field(boolField, "Filter to flagged (true) or unflagged (false) messages."),
		"sort":                    field(map[string]any{"type": "string", "enum": []string{"received_desc", "received_asc"}}, "Result order; defaults to received_desc (newest first)."),
		"message_ids":             field(strArray, "The exact messages, ids from a mail_search result."),
		"max_body_bytes":          field(intField, "Optionally lowers the configured body cap; it can never raise it."),
		"calendar_ids":            field(strArray, "The calendars to query, ids from a calendar_list result."),
		"start":                   field(str, "RFC3339 window start."),
		"end":                     field(str, "RFC3339 window end."),
		"timezone":                field(str, "IANA zone the window and results are interpreted in."),
		"event_id":                field(str, "The single event, an id from a calendar_events result."),
		"duration_minutes":        field(intField, "Minimum contiguous free-slot length, in minutes."),
		"buffer_minutes":          field(intField, "Shrink each candidate slot by this many minutes on each side."),
		"include_all_day_as_busy": field(boolField, "Treat all-day events as busy when true."),
		"working_hours": map[string]any{
			"type":        "object",
			"description": `{"start":"HH:MM","end":"HH:MM","days":["mon",...]} local clock times; days defaults to Monday-Friday.`,
			"properties": map[string]any{
				"start": str,
				"end":   str,
				"days":  strArray,
			},
		},
		"item_id": field(str, "The item to open in its owning app, an id from any prior read result."),
		"changes": map[string]any{
			"type":        "array",
			"description": "One or more change objects, each with its own \"type\" field (mail_move, mail_set_read, mail_set_flag, mail_save_draft, calendar_create_event, or calendar_update_event) — see the Personal Mail/Calendar rules for each type's exact fields. These are values for \"type\" inside this array, never tool names to call by themselves.",
			"items":       map[string]any{"type": "object"},
		},
		"plan_id": field(str, "The plan_id a prior change_prepare returned, after a human approved it in a separate turn."),
	}
}

// personalAppsFieldSet names which fields one operation accepts.
type personalAppsFieldSet struct {
	required []string
	optional []string
}

// personalAppsOperationFieldSets mirrors each Args type's own json tags in
// internal/personalapps/request.go. It exists only to shape the advertised
// schema — a field missing here or misnamed only makes that field
// undiscoverable from the schema text, since ParseRequest independently
// validates the real call against the real struct regardless of what this
// package advertises.
var personalAppsOperationFieldSets = map[personalapps.Operation]personalAppsFieldSet{
	personalapps.OpStatus:       {},
	personalapps.OpMailAccounts: {optional: []string{"limit", "cursor"}},
	personalapps.OpMailMailboxes: {
		required: []string{"account_id"},
		optional: []string{"parent_id", "limit", "cursor"},
	},
	personalapps.OpMailSearch: {
		optional: []string{
			"account_ids", "mailbox_ids", "received_after", "received_before",
			"from", "subject", "unread", "flagged", "sort", "limit", "cursor",
		},
	},
	personalapps.OpMailRead:     {required: []string{"message_ids"}, optional: []string{"max_body_bytes"}},
	personalapps.OpCalendarList: {optional: []string{"limit", "cursor"}},
	personalapps.OpCalendarEvents: {
		required: []string{"calendar_ids", "start", "end", "timezone"},
		optional: []string{"limit", "cursor"},
	},
	personalapps.OpCalendarEvent: {required: []string{"event_id"}},
	personalapps.OpCalendarFreeSlots: {
		required: []string{"calendar_ids", "start", "end", "timezone", "duration_minutes", "working_hours"},
		optional: []string{"buffer_minutes", "include_all_day_as_busy"},
	},
	personalapps.OpChangePrepare: {required: []string{"changes"}},
	personalapps.OpChangeApply:   {required: []string{"plan_id"}},
	personalapps.OpOpenItem:      {required: []string{"item_id"}},
}

// personalAppsOperationSchema builds one operation's flat JSON Schema from
// personalAppsFieldSchemas and personalAppsOperationFieldSets.
func personalAppsOperationSchema(op personalapps.Operation) json.RawMessage {
	fields := personalAppsFieldSchemas()
	set := personalAppsOperationFieldSets[op]
	properties := make(map[string]any, len(set.required)+len(set.optional))
	for _, name := range set.required {
		properties[name] = fields[name]
	}
	for _, name := range set.optional {
		properties[name] = fields[name]
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(set.required) > 0 {
		schema["required"] = set.required
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		// Every value above is a static literal; Marshal cannot fail on it.
		panic(fmt.Sprintf("build %s schema: %v", op, err))
	}
	return raw
}

// MaxPersonalAppsPayloadBytes bounds the raw personal_apps envelope this
// package will hand to internal/personalapps.ParseRequest. It mirrors that
// package's own documented default request-size limit (256 KiB); the real,
// authoritative bound is whatever Limits the wired Service was constructed
// with, applied inside ParseRequest itself.
const MaxPersonalAppsPayloadBytes = 256 * 1024

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
		out = append(out, provider.Message{
			Role:       provider.RoleTool,
			Content:    formatResultContent(res),
			ToolCallID: res.Call.ID,
			ToolName:   res.Call.Tool,
			Display:    res.Diff,
			References: append([]provider.MessageReference(nil), res.References...),
		})
	}
	return out
}

// LimitResults builds the results for a batch that was not executed because
// the per-turn iteration budget ran out. Instead of dead-ending the turn, it
// tells the model to wrap up, so the user still gets a final answer.
func LimitResults(calls []Call, max int) []Result {
	err := fmt.Errorf("tool iteration limit reached (%d rounds this turn, tools.max_iterations) — this call was not executed. Do not request more tools; give your final answer now using what you already know", max)
	meta := ResultMeta{
		Outcome: OutcomeUnknown,
		Effect:  EffectUnknown,
		Error:   &ErrorInfo{Code: "budget_block", Retry: RetryLater, Message: boundErrorMessage(err)},
	}
	out := make([]Result, len(calls))
	for i, c := range calls {
		out[i] = Result{Call: c, Err: err, Meta: meta}
	}
	return out
}

// NativeInstructions is appended to the system prompt when tools are offered
// natively; the protocol itself needs no explanation, only the house rules.
// withWeb adds the web-tool rules when the user has turned them on.
//
// Per-argument mechanics (offset/limit, byte_offset, resource_id,
// expected_resource_id, dry_run) live in each tool's own schema, which a
// native model reads with the call. This text only carries what a schema
// cannot: which tool to prefer, cross-tool rules, and safety boundaries. MCP
// discovery rules travel with the compact MCP directory
// (internal/tui/tool_search.go), so a session without hidden MCP tools does
// not pay for them. See docs/prompt-composition.md "Tool guidance".
func NativeInstructions(root string, withWeb bool) string {
	webRules := ""
	if withWeb {
		webRules = "\n\n" + webInstructions
	}
	return strings.TrimSpace(fmt.Sprintf(`You can work with files in the user's project directory (%s) using the provided tools.

`+toolChoiceInstructions+`
Rules:
- Paths are relative to the project root; never use absolute paths or "..".
- Writes and non-read-only commands may require the user's approval; a denied action returns "denied by the user" — respect it and continue without that action.
`+askUserInstructions+`
- Only call a tool when you need it. When the task is complete, reply with your final answer and no tool calls.%s`, root, webRules))
}

// toolChoiceInstructions tells a model which built-in tool fits which job.
// Shared by both protocols. Small local models otherwise default to
// run_command for everything (cat, ls, find, grep, sed -i), which costs an
// approval prompt, bypasses the file tools' secret-file and .git handling,
// and returns less structured output that stays in the context for every
// later request.
const toolChoiceInstructions = `Choosing tools:
- Find files by name: glob. Search file contents: grep. See a folder's layout: directory_tree, or list_dir for one level. Metadata only: get_file_info.
- Read one file with read_file (offset/limit for part of a large file); read several at once with read_multiple_files.
- Change an existing file with edit_file; use write_file only to create a file or replace all of it. Organize with create_directory and move_file.
- Use run_command only for what the file tools cannot do — builds, tests, git, package managers — not to read, list, search, or edit files. It takes exactly one command line; save a multi-line script with write_file first.
- For the current date, time, timezone, weekday, or relative dates (today, tomorrow, yesterday, next Monday, deadlines, schedules), call local_context with kind=time; never infer the current date from training knowledge.
- Put independent read-only calls in one batch. Do not repeat a call whose result is already in the conversation.
`
