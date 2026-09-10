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
	gemmaCallStart                = regexp.MustCompile(`call:[A-Za-z_][A-Za-z0-9_.-]*\s*\{`)
	gemmaZeroArgumentCall         = regexp.MustCompile(`call:([A-Za-z_][A-Za-z0-9_.-]*)\{\s*\}`)
	gemmaAlternateToolcallTokenRE = regexp.MustCompile(`<\|toolcall\|>?`)

	// gemmaOrphanChannelTokenRE matches a Gemma 4 channel-boundary token
	// (<|channel>NAME opening, or <channel|> closing) so it can be stripped
	// from streamed text before the user ever sees it. yzma's own cleanup
	// (message.StripMarkup) only removes a matched
	// <|channel>thought...<channel|> pair used for internal reasoning; a
	// channel opened under any other name (e.g. "final"), or a lone
	// unpaired marker the model emits mid-answer (observed: the model
	// repeating its whole answer with a bare <channel|> in between),
	// survives untouched and would otherwise leak into the visible reply
	// as literal control-token text. This is a defensive text-only strip:
	// it removes the marker, never the content around it.
	gemmaOrphanChannelTokenRE = regexp.MustCompile(`<\|channel>[A-Za-z0-9_]*|<channel\|>`)
)

// gemmaChannelTokenMarkers are the literal channel-boundary tokens passed to
// retainedDelimiterSuffix so a partial occurrence at the tail of pending
// streamed text is held back rather than split across two emitted chunks.
var gemmaChannelTokenMarkers = []string{"<|channel>", "<channel|>"}

// incompleteGemmaChannelTokenIndex returns the index of a complete
// "<|channel>" opening token at the tail of value whose NAME suffix cannot
// yet be resolved (more word characters may still follow), mirroring
// incompleteGemmaCallIndex for "call:NAME{". Returns -1 when there is
// nothing pending.
func incompleteGemmaChannelTokenIndex(value string) int {
	const open = "<|channel>"
	index := strings.LastIndex(value, open)
	if index < 0 {
		return -1
	}
	tail := value[index+len(open):]
	for _, char := range tail {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return -1 // a non-word character already terminated the name
	}
	return index
}

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
		// Hold back a channel-boundary token still being typed (a partial
		// "<|channel>" / "<channel|>" literal, or a complete "<|channel>"
		// whose NAME suffix might still grow) so it is never split across
		// two emitted chunks — a split token can't be recognised as a
		// complete token to strip once its pieces have already been sent.
		if suffix := retainedDelimiterSuffix(r.pending, gemmaChannelTokenMarkers); suffix > retained {
			retained = suffix
		}
		if index := incompleteGemmaChannelTokenIndex(r.pending); index >= 0 && len(r.pending)-index > retained {
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
		return nil, nil, &embedded.MalformedToolCallError{Format: r.format}
	}
	if len(parsed) > 0 {
		if err := validateUnrepairedJSONToolBlocks(raw, r.format); err != nil {
			return nil, nil, err
		}
		if r.format == embedded.ToolFormatGemma {
			parsed = repairGemmaBracketSplitArgs(raw, parsed)
			if gemmaSwallowedKey(parsed) || gemmaImpossibleKey(parsed) {
				return nil, nil, &embedded.MalformedToolCallError{Format: r.format}
			}
		}
	}
	calls, err := normalizeToolCalls(parsed, r.tools)
	if err != nil {
		return nil, nil, err
	}

	cleaned := message.StripMarkup(raw)
	if r.format == embedded.ToolFormatGemma {
		// Keep this comparable to r.emitted, which emit() already strips of
		// the same tokens as they stream — otherwise a stray channel token
		// only present in cleaned (never live-streamed) would either break
		// the prefix match in unstreamedText or leak into the tail.
		cleaned = gemmaOrphanChannelTokenRE.ReplaceAllString(cleaned, "")
		cleaned = gemmaAlternateToolcallTokenRE.ReplaceAllString(cleaned, "")
	}
	tail := unstreamedText(cleaned, r.emitted)
	r.pending = ""
	if tail == "" {
		return nil, calls, nil
	}
	return []string{tail}, calls, nil
}

// gemmaSwallowedKeyRE matches the residue yzma's parseGemmaArgs leaves in a
// value when the model omits that value's closing quote token: the parser
// runs past the delimiter and consumes the following `key:<quote>` pair —
// and every pair after it — into the previous value. Reproduced live with a
// Gemma 4 MoE finetune at temperature 0.8 emitting calendar_events, where
// "start" absorbed `, end:<|"|>...` and the call arrived looking structurally
// valid but silently missing "end".
//
// It deliberately requires the quote token after the colon, so a genuine
// bracketed array of quote-wrapped elements ("[<|\"|>a<|\"|>, <|\"|>b<|\"|>]",
// which repairScalarAsArray recovers) never matches: there the comma is
// followed by a quote token directly, never by an identifier and a colon.
var gemmaSwallowedKeyRE = regexp.MustCompile(`,\s*"?[A-Za-z_][A-Za-z0-9_.-]*"?\s*:\s*(?:<\|"\|>|<">|<\|>)`)

// gemmaSwallowedKey reports whether any parsed argument value absorbed a
// following key/value pair. Such a call must be rejected as malformed rather
// than dispatched or diagnosed as a missing required argument: the arguments
// that vanished are not arguments the model declined to send, and telling it
// one is "missing" invites the identical retry that the repeated-call guard
// then blocks. Reporting malformed instead routes it into the existing
// one-shot retry and fenced-protocol fallback.
func gemmaSwallowedKey(calls []message.ToolCall) bool {
	for _, call := range calls {
		for _, value := range call.Function.Arguments {
			if gemmaSwallowedKeyRE.MatchString(value) {
				return true
			}
		}
	}
	return false
}

// gemmaArgumentKeyRE matches a key a Gemma call can actually name: the same
// identifier shape yzma's own key detection accepts, anchored. A parsed key
// that fails it did not come from the model — it is parser residue.
var gemmaArgumentKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// gemmaImpossibleKey reports whether any parsed call carries an argument key
// no model could have emitted. Like gemmaSwallowedKey, such a call must be
// reported malformed rather than dispatched or diagnosed as a missing
// argument: the real key the residue absorbed is not one the model declined
// to send, so "missing" invites the identical retry the repeated-call guard
// then ends the turn on.
func gemmaImpossibleKey(calls []message.ToolCall) bool {
	for _, call := range calls {
		for key := range call.Function.Arguments {
			if !gemmaArgumentKeyRE.MatchString(key) {
				return true
			}
		}
	}
	return false
}

// repairGemmaBracketSplitArgs recovers the arguments yzma's parseGemmaArgs
// loses when a value is a bracketed array with more than one element. That
// parser has no case for "[" at all, so its bare-value fallback reads to the
// first top-level comma — which lands inside the array. The remainder of the
// array, plus the key that follows it, is then read as a single nonsense key,
// and the following argument silently disappears.
//
// Reproduced live on calendar_events against three calendars: the model
// emitted a correct call:calendar_events{calendar_ids:[<|"|>a<|"|>,
// <|"|>b<|"|>], start:…, end:…} and llmtui saw "end" as missing, so the model
// retried the identical call until the repeated-call guard ended the turn.
//
// The repair re-parses only the calls yzma provably mangled (detected by
// gemmaImpossibleKey), from the same raw text, with a scanner that treats
// "[…]" as one balanced value. Raw blocks are consumed for every parsed call,
// including calls that need no repair, so repeated calls with the same name
// cannot borrow arguments from an earlier occurrence. Everything yzma parsed
// cleanly is left exactly as it was. A re-parse that cannot produce well-formed
// keys is discarded, so the malformed path still catches genuinely corrupt
// output.
func repairGemmaBracketSplitArgs(raw string, calls []message.ToolCall) []message.ToolCall {
	blocks := gemmaArgumentBlocks(raw)
	if len(blocks) == 0 {
		return calls
	}
	byName := make(map[string][]gemmaArgumentBlock, len(blocks))
	for _, block := range blocks {
		// yzma omits empty calls from a mixed response, so they must not
		// consume a position in the queue aligned to its parsed calls.
		if strings.TrimSpace(block.arguments) == "" {
			continue
		}
		byName[block.name] = append(byName[block.name], block)
	}
	repaired := append([]message.ToolCall(nil), calls...)
	for index, call := range repaired {
		queue := byName[call.Function.Name]
		if len(queue) == 0 {
			continue
		}
		block := queue[0]
		byName[call.Function.Name] = queue[1:]
		if !gemmaBracketSplitSuspect(call) {
			continue
		}
		arguments := parseGemmaBracketAwareArgs(block.arguments)
		if len(arguments) > 0 {
			repaired[index].Function.Arguments = arguments
		}
	}
	return repaired
}

// gemmaBracketSplitSuspect reports whether a parsed call carries the
// signature of that gap: parser residue in a key, or a value that still
// begins with "[" because the bare-value fallback stopped at the array's
// first inner comma.
func gemmaBracketSplitSuspect(call message.ToolCall) bool {
	for key, value := range call.Function.Arguments {
		if !gemmaArgumentKeyRE.MatchString(key) {
			return true
		}
		if strings.HasPrefix(strings.TrimSpace(value), "[") {
			return true
		}
	}
	return false
}

type gemmaArgumentBlock struct {
	name      string
	arguments string
}

// gemmaArgumentBlocks locates every call:NAME{…} block in raw, in order,
// pairing each name with its balanced brace contents.
func gemmaArgumentBlocks(raw string) []gemmaArgumentBlock {
	var blocks []gemmaArgumentBlock
	remaining := raw
	for {
		location := gemmaCallStart.FindStringIndex(remaining)
		if location == nil {
			return blocks
		}
		start := location[0]
		brace := location[1] - 1
		name := strings.TrimSpace(remaining[start+len("call:") : brace])
		remaining = remaining[brace:]
		end := gemmaBalancedEnd(remaining, '{', '}')
		if end < 0 {
			blocks = append(blocks, gemmaArgumentBlock{name: name, arguments: remaining[1:]})
			return blocks
		}
		blocks = append(blocks, gemmaArgumentBlock{name: name, arguments: remaining[1:end]})
		remaining = remaining[end+1:]
	}
}

// gemmaBalancedEnd returns the index of the delimiter closing the one at
// s[0], skipping over quote-token pairs so a delimiter inside a value never
// closes the block. It returns -1 when the block is unterminated.
func gemmaBalancedEnd(s string, open, close byte) int {
	if len(s) == 0 || s[0] != open {
		return -1
	}
	depth := 0
	for index := 0; index < len(s); {
		if width := gemmaQuotedValueWidth(s[index:]); width > 0 {
			index += width
			continue
		}
		switch s[index] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return index
			}
		}
		index++
	}
	return -1
}

// gemmaQuotedValueWidth returns the byte length of a complete quote-token
// delimited value at the start of s, or 0 when s does not start with one.
func gemmaQuotedValueWidth(s string) int {
	for _, token := range gemmaQuoteTokens {
		if !strings.HasPrefix(s, token) {
			continue
		}
		closing := strings.Index(s[len(token):], token)
		if closing < 0 {
			return 0
		}
		return len(token) + closing + len(token)
	}
	return 0
}

// parseGemmaBracketAwareArgs parses one Gemma argument block the way
// parseGemmaArgs does, except that "[…]" and "{…}" values are kept whole as
// one balanced value instead of being cut at their first inner comma. The
// bracketed result is what repairScalarAsArray already knows how to turn into
// a real array. It returns nil the moment the block stops looking like
// key:value pairs, so a hopeless parse is never mistaken for a repair.
func parseGemmaBracketAwareArgs(raw string) map[string]string {
	arguments := make(map[string]string)
	remaining := raw
	for {
		remaining = strings.TrimLeft(remaining, ", \t\r\n")
		if remaining == "" {
			return arguments
		}
		colon := strings.Index(remaining, ":")
		if colon < 0 {
			return nil
		}
		key := strings.Trim(strings.TrimSpace(remaining[:colon]), `"`)
		if !gemmaArgumentKeyRE.MatchString(key) {
			return nil
		}
		remaining = strings.TrimSpace(remaining[colon+1:])

		if width := gemmaQuotedValueWidth(remaining); width > 0 {
			token := gemmaQuoteTokenPrefix(remaining)
			arguments[key] = remaining[len(token) : width-len(token)]
			remaining = remaining[width:]
			continue
		}
		if token := gemmaQuoteTokenPrefix(remaining); token != "" {
			// Unterminated value: keep the residue so the swallowed-key
			// check still sees the same corruption it does today.
			arguments[key] = remaining[len(token):]
			return arguments
		}
		if remaining[0] == '[' || remaining[0] == '{' {
			open, close := byte('['), byte(']')
			if remaining[0] == '{' {
				open, close = '{', '}'
			}
			end := gemmaBalancedEnd(remaining, open, close)
			if end < 0 {
				return nil
			}
			arguments[key] = remaining[:end+1]
			remaining = remaining[end+1:]
			continue
		}
		if remaining[0] == '"' {
			end := strings.Index(remaining[1:], `"`)
			if end < 0 {
				return nil
			}
			arguments[key] = remaining[1 : end+1]
			remaining = remaining[end+2:]
			continue
		}
		end := strings.IndexAny(remaining, ",}")
		if end < 0 {
			arguments[key] = strings.TrimSpace(remaining)
			return arguments
		}
		arguments[key] = strings.TrimSpace(remaining[:end])
		remaining = remaining[end:]
	}
}

func gemmaQuoteTokenPrefix(s string) string {
	for _, token := range gemmaQuoteTokens {
		if strings.HasPrefix(s, token) {
			return token
		}
	}
	return ""
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
	if r.format == embedded.ToolFormatGemma {
		text = gemmaOrphanChannelTokenRE.ReplaceAllString(text, "")
		text = gemmaAlternateToolcallTokenRE.ReplaceAllString(text, "")
	}
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
		return []string{"<|toolcall>", "<|toolcall|", "<toolcall>", "<|tool_call>", "<tool_call>", `{"name"`}
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

// mcpToolNamePrefix mirrors internal/tools.SplitMCPToolName's naming shape
// ("mcp__server__tool"). It is duplicated here, as a shape check only, rather
// than imported: internal/provider/embedded/llamart must stay a
// minimal-dependency leaf package (CLAUDE.md architecture note 2), and
// internal/tools sits above it, so this package cannot import that one.
const mcpToolNamePrefix = "mcp__"

// looksLikeMCPToolName reports whether name has the shape an MCP-routed tool
// call name always has, without resolving it to any real server or tool.
func looksLikeMCPToolName(name string) bool {
	rest, ok := strings.CutPrefix(name, mcpToolNamePrefix)
	if !ok {
		return false
	}
	server, tool, ok := strings.Cut(rest, "__")
	return ok && server != "" && tool != ""
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
			notOffered := &provider.ToolNotOfferedError{
				RequestedName: name,
				OfferedNames:  offeredToolNames(tools),
			}
			if looksLikeMCPToolName(name) {
				// internal/tui's hiddenMCPToolRecoveryName auto-discloses and
				// retries exactly this shape, but only in response to a hard
				// EventError carrying this type — so this one case must keep
				// failing the whole generation rather than degrade below.
				return nil, notOffered
			}
			// Any other unknown name (observed live: Gemma 4 calling
			// mail_save_draft/calendar_create_event directly — those are
			// change_prepare change types, never tools) is a mistake the
			// model can see and correct within the same turn, the same way
			// a missing or invalid argument already is a couple of lines
			// down. Ending the whole generation over it, the way a hard
			// error does, gives the model no chance to recover at all.
			result = append(result, provider.ToolCall{Name: name, ArgumentsError: notOffered.Error()})
			continue
		}
		schema, err := decodeJSONObject(spec.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool %q parameters must be a JSON object: %w", name, err)
		}
		if missing, err := missingRequiredArgument(schema, call.Function.Arguments); err != nil {
			return nil, fmt.Errorf("tool %q parameters are invalid: %w", name, err)
		} else if missing != "" {
			result = append(result, provider.ToolCall{
				Name:           name,
				ArgumentsError: fmt.Sprintf("call is missing required argument %q", missing),
			})
			continue
		}
		arguments := make(map[string]any, len(call.Function.Arguments))
		var argErr error
		for key, raw := range call.Function.Arguments {
			value, err := normalizeArgument(raw, propertySchema(schema, key))
			if err != nil {
				argErr = fmt.Errorf("argument %q is invalid: %w", key, err)
				break
			}
			arguments[key] = value
		}
		if argErr != nil {
			result = append(result, provider.ToolCall{Name: name, ArgumentsError: argErr.Error()})
			continue
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
			if typeName == "array" {
				if v, ok := repairScalarAsArray(raw, schema); ok {
					return v, nil
				}
			}
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
			if v, ok := repairScalarAsArray(raw, schema); ok {
				return v, nil
			}
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

// repairScalarAsArray handles a common small-model tool-calling mistake:
// emitting one bare or JSON-quoted scalar for an array-typed argument
// instead of wrapping it in []. Reproduced live with Gemma 4 E4B via
// mail_search's account_ids/mailbox_ids (both string arrays) — the model
// consistently omitted the brackets for a single id.
//
// It only fires when raw is exactly one value of the array's own declared
// element type (schema["items"]) — never for an untyped, array-of-array or
// array-of-object schema, where wrapping could silently accept something
// the caller never intended. It never widens what a well-formed multi-item
// array argument would already do: a raw value that itself decodes as a
// JSON array never reaches this function (see normalizeArgument's two call
// sites), so a model that got the shape right is never second-guessed.
func repairScalarAsArray(raw string, schema map[string]any) (any, bool) {
	items, _ := schema["items"].(map[string]any)
	itemType, _ := items["type"].(string)
	switch strings.ToLower(itemType) {
	case "string", "integer", "number", "boolean":
	default:
		return nil, false
	}
	// A model that does try to wrap a value in [] often still reaches this
	// function, not with a clean JSON array, but with the whole bracketed
	// expression as one raw string, Gemma quote tokens and all —
	// "[<|\"|>box_1<|\"|>]" — because github.com/hybridgroup/yzma's Gemma
	// text-call parser (parseGemmaArgs) recognizes Gemma-quote-wrapped
	// values, JSON-double-quoted values and nested {...} objects, but has
	// no case for "[" at all; its bare-value fallback just reads straight
	// through to the next top-level comma/brace. That is a gap in that
	// dependency, not something to patch here — this recovers the model's
	// actual intent from the same delimiter vocabulary yzma itself already
	// recognizes, entirely on this side.
	if elems, ok := splitGemmaBracketedArray(raw); ok {
		out := make([]any, 0, len(elems))
		for _, elem := range elems {
			v, err := normalizeArgument(elem, items)
			if err != nil {
				return nil, false
			}
			out = append(out, v)
		}
		return out, true
	}
	if json.Valid([]byte(raw)) {
		var decoded any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&decoded); err != nil {
			return nil, false
		}
		if _, isArray := decoded.([]any); isArray {
			// Already a well-formed array; nothing to repair.
			return nil, false
		}
		if v, ok := wrapIfMatchesItemType(decoded, itemType); ok {
			return v, true
		}
		return nil, false
	}
	// raw is not valid JSON on its own (e.g. an unquoted bareword like a
	// handle) — try it as one raw scalar of the item's type, with the same
	// strict coercion a genuine single-value argument of that type gets.
	value, err := normalizeArgument(raw, items)
	if err != nil {
		return nil, false
	}
	return []any{value}, true
}

// gemmaQuoteTokens mirrors, deliberately, the exact delimiter set
// github.com/hybridgroup/yzma/pkg/message's parseGemmaArgs recognizes
// (longest first, matching that package's own ordering) — this only ever
// needs to undo what that parser's bare-value fallback left intact, never
// to recognize a delimiter yzma itself would not have.
var gemmaQuoteTokens = []string{"<|\"|>", "<\">", "<|>"}

// splitGemmaBracketedArray recognizes raw as a "[elem, elem, ...]" array
// attempt whose elements are Gemma-quote-token-delimited or bare — the one
// shape yzma's Gemma parser leaves completely unparsed, brackets and quote
// tokens intact, as a single string. It splits on top-level commas only
// (never one inside a quote-token pair) and strips each element's own
// quote-token or JSON-double-quote wrapping. ok is false for anything that
// is not clearly this shape — no leading "[whatever]" text is ever misread
// as an array by accident — including an empty "[]", which has nothing to
// repair.
func splitGemmaBracketedArray(raw string) ([]string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, false
	}
	inner := trimmed[1 : len(trimmed)-1]
	if strings.TrimSpace(inner) == "" {
		return nil, false
	}

	var pieces []string
	start := 0
	i := 0
	for i < len(inner) {
		matchedToken := false
		for _, tok := range gemmaQuoteTokens {
			if !strings.HasPrefix(inner[i:], tok) {
				continue
			}
			closeIdx := strings.Index(inner[i+len(tok):], tok)
			if closeIdx == -1 {
				// An unterminated quote token inside the brackets means
				// this isn't the clean shape this function repairs.
				return nil, false
			}
			i += len(tok) + closeIdx + len(tok)
			matchedToken = true
			break
		}
		if matchedToken {
			continue
		}
		if inner[i] == ',' {
			pieces = append(pieces, inner[start:i])
			i++
			start = i
			continue
		}
		i++
	}
	pieces = append(pieces, inner[start:])

	out := make([]string, 0, len(pieces))
	for _, p := range pieces {
		out = append(out, stripGemmaQuoteWrapping(p))
	}
	return out, true
}

// stripGemmaQuoteWrapping removes one layer of Gemma-quote-token or
// standard JSON double-quote wrapping from s, or returns s unchanged (a
// bare, unquoted value — also valid inside a Gemma array attempt).
func stripGemmaQuoteWrapping(s string) string {
	s = strings.TrimSpace(s)
	for _, tok := range gemmaQuoteTokens {
		if strings.HasPrefix(s, tok) && strings.HasSuffix(s, tok) && len(s) >= 2*len(tok) {
			return s[len(tok) : len(s)-len(tok)]
		}
	}
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		return s[1 : len(s)-1]
	}
	return s
}

func wrapIfMatchesItemType(value any, itemType string) (any, bool) {
	switch strings.ToLower(itemType) {
	case "string":
		if _, ok := value.(string); ok {
			return []any{value}, true
		}
	case "integer", "number":
		if _, ok := value.(json.Number); ok {
			return []any{value}, true
		}
	case "boolean":
		if _, ok := value.(bool); ok {
			return []any{value}, true
		}
	}
	return nil, false
}

func propertySchema(schema map[string]any, key string) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	property, _ := properties[key].(map[string]any)
	return property
}

func offeredToolNames(tools []provider.ToolSpec) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
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
			return &embedded.MalformedToolCallError{Format: format}
		}
		if end < 0 {
			return nil
		}
		normalized = rest[end+len("</tool_call>"):]
	}
}
