package llamart

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/hybridgroup/yzma/pkg/message"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

var (
	gemmaCallStart        = regexp.MustCompile(`call:[A-Za-z_][A-Za-z0-9_.-]*\{`)
	gemmaZeroArgumentCall = regexp.MustCompile(`call:([A-Za-z_][A-Za-z0-9_.-]*)\{\s*\}`)
)

const gemmaToolFollowupInstruction = "After the tool returns, use its result to answer this request unless another tool call is necessary."

type canonicalTool struct {
	Type     string                `json:"type"`
	Function canonicalToolFunction `json:"function"`
}

type canonicalToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// prepareToolMessages gives templates a deterministic grammar instruction in
// addition to their native `tools` variable. This keeps tool use available for
// valid templates that expose tools but do not explain their output grammar.
func prepareToolMessages(messages []provider.Message, tools []provider.ToolSpec, format embedded.ToolFormat) ([]provider.Message, error) {
	if len(tools) == 0 {
		return messages, nil
	}
	definitions := make([]canonicalTool, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, errors.New("offered tool has an empty name")
		}
		if _, exists := seen[tool.Name]; exists {
			return nil, fmt.Errorf("tool %q is offered more than once", tool.Name)
		}
		seen[tool.Name] = struct{}{}
		if _, err := decodeJSONObject(tool.Parameters); err != nil {
			return nil, fmt.Errorf("tool %q parameters must be a JSON object: %w", tool.Name, err)
		}
		parameters := tool.Parameters
		if len(bytes.TrimSpace(parameters)) == 0 {
			parameters = json.RawMessage(`{}`)
		}
		definitions = append(definitions, canonicalTool{
			Type: "function",
			Function: canonicalToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	encoded, err := json.Marshal(definitions)
	if err != nil {
		return nil, fmt.Errorf("encode tool definitions: %w", err)
	}
	instruction := "Available tools (JSON): " + string(encoded) + "\n" + toolGrammarInstruction(format)

	prepared := append([]provider.Message(nil), messages...)
	if format == embedded.ToolFormatGemma {
		for index := len(prepared) - 1; index >= 0; index-- {
			if prepared[index].Role != provider.RoleUser {
				continue
			}
			if !strings.Contains(prepared[index].Content, gemmaToolFollowupInstruction) {
				prepared[index].Content = strings.TrimSpace(prepared[index].Content) + "\n\n" + gemmaToolFollowupInstruction
			}
			break
		}
	}
	if len(prepared) > 0 && prepared[0].Role == provider.RoleSystem {
		prepared[0].Content = strings.TrimSpace(prepared[0].Content) + "\n\n" + instruction
		return prepared, nil
	}
	return append([]provider.Message{{Role: provider.RoleSystem, Content: instruction}}, prepared...), nil
}

func toolGrammarInstruction(format embedded.ToolFormat) string {
	switch format {
	case embedded.ToolFormatQwen:
		return "To call a tool, emit <function=NAME><parameter=KEY>VALUE</parameter></function>. Do not invent tool results."
	case embedded.ToolFormatGLM:
		return "To call a tool, emit NAME<arg_key>KEY</arg_key><arg_value>VALUE</arg_value>. Do not invent tool results."
	case embedded.ToolFormatMistral:
		return "To call a tool, emit [TOOL_CALLS]NAME[ARGS]{\"key\":value}. Do not invent tool results."
	case embedded.ToolFormatGemma:
		return "To call a tool, emit <|toolcall>call:NAME{key:<|\"|>value<|\"|>}<toolcall|>. Do not invent tool results."
	case embedded.ToolFormatGPT:
		return "To call a tool, emit .NAME <|message|>{\"key\":value}. Do not invent tool results."
	case embedded.ToolFormatPhi:
		return "To call a tool, emit <|tool_call>{\"name\":\"NAME\",\"arguments\":{}} </tool_call>. Do not invent tool results."
	default:
		return "To call a tool, emit <tool_call>{\"name\":\"NAME\",\"arguments\":{}}</tool_call>. Do not invent tool results."
	}
}

// toolOutputRouter streams safe ordinary text while retaining possible tool
// delimiters. It always keeps the complete raw non-reasoning output for yzma's
// grammar parser and emits tool calls only from Finish.
type toolOutputRouter struct {
	format  embedded.ToolFormat
	tools   []provider.ToolSpec
	raw     strings.Builder
	pending string
	emitted string
	intent  bool
}

func newToolOutputRouter(format embedded.ToolFormat, tools []provider.ToolSpec) *toolOutputRouter {
	return &toolOutputRouter{format: format, tools: tools}
}

func (r *toolOutputRouter) Push(text string) []string {
	if text == "" {
		return nil
	}
	r.raw.WriteString(text)
	r.pending += text
	if r.intent {
		return nil
	}

	if index := definiteToolIntentIndex(r.pending, r.format); index >= 0 {
		r.intent = true
		return r.emit(r.pending[:index])
	}
	if lineBufferedToolFormat(r.format) {
		lastNewline := strings.LastIndexByte(r.pending, '\n')
		if lastNewline < 0 {
			return nil
		}
		return r.emitPending(lastNewline + 1)
	}

	markers := toolStartMarkers(r.format)
	retained := retainedDelimiterSuffix(r.pending, markers)
	if r.format == embedded.ToolFormatGemma {
		if index := incompleteGemmaCallIndex(r.pending); index >= 0 && len(r.pending)-index > retained {
			retained = len(r.pending) - index
		}
	}
	return r.emitPending(len(r.pending) - retained)
}

func (r *toolOutputRouter) Finish() ([]string, []provider.ToolCall, error) {
	raw := r.raw.String()
	parsed := message.ParseToolCalls(raw)
	if len(parsed) == 0 && r.format == embedded.ToolFormatGemma {
		parsed = parseGemmaZeroArgumentCalls(raw)
	}
	recognized := r.intent || definiteToolIntentIndex(raw, r.format) >= 0
	if len(parsed) == 0 && recognized {
		return nil, nil, fmt.Errorf("model emitted a recognizable but malformed %s tool call", r.format)
	}
	if len(parsed) > 0 {
		if err := validateUnrepairedJSONToolBlocks(raw, r.format); err != nil {
			return nil, nil, err
		}
	}
	calls, err := normalizeToolCalls(parsed, r.tools)
	if err != nil {
		return nil, nil, err
	}

	cleaned := message.StripMarkup(raw)
	tail := unstreamedText(cleaned, r.emitted)
	r.pending = ""
	if tail == "" {
		return nil, calls, nil
	}
	return []string{tail}, calls, nil
}

// parseGemmaZeroArgumentCalls handles valid call:name{} output that yzma's
// Gemma parser intentionally omits. It returns calls only when every Gemma
// call-shaped block in the response is a complete empty-object call; a mixed
// or truncated response must continue down the malformed-output path.
func parseGemmaZeroArgumentCalls(raw string) []message.ToolCall {
	starts := gemmaCallStart.FindAllStringIndex(raw, -1)
	matches := gemmaZeroArgumentCall.FindAllStringSubmatch(raw, -1)
	if len(starts) == 0 || len(matches) != len(starts) {
		return nil
	}
	calls := make([]message.ToolCall, 0, len(matches))
	for _, match := range matches {
		calls = append(calls, message.ToolCall{
			Type: "function",
			Function: message.ToolFunction{
				Name:      match[1],
				Arguments: map[string]string{},
			},
		})
	}
	return calls
}

func (r *toolOutputRouter) emitPending(length int) []string {
	if length <= 0 {
		return nil
	}
	text := r.pending[:length]
	r.pending = r.pending[length:]
	return r.emit(text)
}

func (r *toolOutputRouter) emit(text string) []string {
	if text == "" {
		return nil
	}
	r.emitted += text
	return []string{text}
}

func toolStartMarkers(format embedded.ToolFormat) []string {
	switch format {
	case embedded.ToolFormatQwen:
		return []string{"<function=", "<tool_call>", "<|tool_call>"}
	case embedded.ToolFormatMistral:
		return []string{"[TOOL_CALLS]"}
	case embedded.ToolFormatGemma:
		return []string{"<|toolcall>", "<toolcall>", "<|tool_call>", "<tool_call>", `{"name"`}
	case embedded.ToolFormatGPT:
		return []string{"<|message|>"}
	case embedded.ToolFormatGLM:
		return []string{"<arg_key>"}
	default:
		return []string{"<tool_call>", "<|tool_call>", `{"name"`}
	}
}

func definiteToolIntentIndex(value string, format embedded.ToolFormat) int {
	index := -1
	for _, marker := range toolStartMarkers(format) {
		if candidate := strings.Index(value, marker); candidate >= 0 && (index < 0 || candidate < index) {
			index = candidate
		}
	}
	if format == embedded.ToolFormatGemma {
		if location := gemmaCallStart.FindStringIndex(value); location != nil && (index < 0 || location[0] < index) {
			index = location[0]
		}
	}
	if index >= 0 && lineBufferedToolFormat(format) {
		index = strings.LastIndex(value[:index], "\n") + 1
	}
	return index
}

func incompleteGemmaCallIndex(value string) int {
	index := strings.LastIndex(value, "call:")
	if index < 0 {
		return -1
	}
	tail := value[index+len("call:"):]
	if tail == "" {
		return index
	}
	for _, char := range tail {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("_.-", char) {
			continue
		}
		return -1
	}
	return index
}

func lineBufferedToolFormat(format embedded.ToolFormat) bool {
	return format == embedded.ToolFormatGLM || format == embedded.ToolFormatGPT
}

func unstreamedText(cleaned, emitted string) string {
	if emitted == "" {
		return cleaned
	}
	if strings.HasPrefix(cleaned, emitted) {
		return cleaned[len(emitted):]
	}
	trimmedEmitted := strings.TrimSpace(emitted)
	if index := strings.Index(cleaned, trimmedEmitted); index >= 0 {
		return strings.TrimSpace(cleaned[index+len(trimmedEmitted):])
	}
	return ""
}

func normalizeToolCalls(parsed []message.ToolCall, tools []provider.ToolSpec) ([]provider.ToolCall, error) {
	if len(parsed) == 0 {
		return nil, nil
	}
	offered := make(map[string]provider.ToolSpec, len(tools))
	for _, tool := range tools {
		offered[tool.Name] = tool
	}
	result := make([]provider.ToolCall, 0, len(parsed))
	for _, call := range parsed {
		name := strings.TrimSpace(call.Function.Name)
		spec, ok := offered[name]
		if !ok {
			return nil, fmt.Errorf("model requested unknown tool %q; offered tools: %s", name, offeredToolNames(tools))
		}
		schema, err := decodeJSONObject(spec.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool %q parameters must be a JSON object: %w", name, err)
		}
		if missing, err := missingRequiredArgument(schema, call.Function.Arguments); err != nil {
			return nil, fmt.Errorf("tool %q parameters are invalid: %w", name, err)
		} else if missing != "" {
			return nil, fmt.Errorf("tool %q call is missing required argument %q", name, missing)
		}
		arguments := make(map[string]any, len(call.Function.Arguments))
		for key, raw := range call.Function.Arguments {
			value, err := normalizeArgument(raw, propertySchema(schema, key))
			if err != nil {
				return nil, fmt.Errorf("tool %q argument %q is invalid: %w", name, key, err)
			}
			arguments[key] = value
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return nil, fmt.Errorf("encode arguments for tool %q: %w", name, err)
		}
		result = append(result, provider.ToolCall{Name: name, Arguments: string(encoded)})
	}
	return result, nil
}

func missingRequiredArgument(schema map[string]any, arguments map[string]string) (string, error) {
	required, exists := schema["required"]
	if !exists {
		return "", nil
	}
	entries, ok := required.([]any)
	if !ok {
		return "", errors.New(`"required" must be an array`)
	}
	for _, entry := range entries {
		name, ok := entry.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return "", errors.New(`"required" entries must be non-empty strings`)
		}
		if _, exists := arguments[name]; !exists {
			return name, nil
		}
	}
	return "", nil
}

func normalizeArgument(raw string, schema map[string]any) (any, error) {
	typeName, _ := schema["type"].(string)
	switch strings.ToLower(typeName) {
	case "string":
		return raw, nil
	case "integer", "number":
		normalized := strings.TrimSpace(raw)
		if !json.Valid([]byte(normalized)) || strings.HasPrefix(normalized, `"`) {
			return nil, errors.New("expected a JSON number")
		}
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(normalized))
		decoder.UseNumber()
		if err := decoder.Decode(&number); err != nil {
			return nil, errors.New("expected a JSON number")
		}
		if typeName == "integer" && strings.ContainsAny(number.String(), ".eE") {
			return nil, errors.New("expected an integer")
		}
		return number, nil
	case "boolean":
		normalized := strings.TrimSpace(raw)
		if normalized == "true" {
			return true, nil
		}
		if normalized == "false" {
			return false, nil
		}
		return nil, errors.New("expected true or false")
	case "object", "array":
		if !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("expected a JSON %s", typeName)
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("expected a JSON %s: %w", typeName, err)
		}
		if typeName == "object" {
			if _, ok := value.(map[string]any); !ok {
				return nil, errors.New("expected a JSON object")
			}
		} else if _, ok := value.([]any); !ok {
			return nil, errors.New("expected a JSON array")
		}
		return value, nil
	default:
		var value any
		if !json.Valid([]byte(raw)) {
			return raw, nil
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err == nil {
			return value, nil
		}
		return raw, nil
	}
}

func propertySchema(schema map[string]any, key string) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	property, _ := properties[key].(map[string]any)
	return property
}

func offeredToolNames(tools []provider.ToolSpec) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ", ")
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	if value == nil {
		return nil, errors.New("expected a JSON object")
	}
	return value, nil
}

func validateUnrepairedJSONToolBlocks(raw string, format embedded.ToolFormat) error {
	if format != embedded.ToolFormatStandard && format != embedded.ToolFormatPhi {
		return nil
	}
	normalized := strings.ReplaceAll(raw, "<|tool_call>", "<tool_call>")
	for {
		start := strings.Index(normalized, "<tool_call>")
		if start < 0 {
			return nil
		}
		rest := normalized[start+len("<tool_call>"):]
		end := strings.Index(rest, "</tool_call>")
		content := rest
		if end >= 0 {
			content = rest[:end]
		}
		if !json.Valid([]byte(strings.TrimSpace(content))) {
			return fmt.Errorf("model emitted malformed %s tool-call JSON", format)
		}
		if end < 0 {
			return nil
		}
		normalized = rest[end+len("</tool_call>"):]
	}
}
