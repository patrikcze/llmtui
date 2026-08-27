package llamart

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

const (
	harmonyStart     = "<|start|>"
	harmonyEnd       = "<|end|>"
	harmonyMessage   = "<|message|>"
	harmonyChannel   = "<|channel|>"
	harmonyConstrain = "<|constrain|>"
	harmonyReturn    = "<|return|>"
	harmonyCall      = "<|call|>"
)

var errMalformedHarmony = errors.New("malformed GPT-OSS Harmony completion")

type harmonyDecodeState uint8

const (
	harmonyHeader harmonyDecodeState = iota
	harmonyContent
	harmonyTerminal
)

// harmonyDecoder is a strict streaming decoder for raw GPT-OSS tokens. It
// interprets protocol markers before any content reaches the provider stream;
// it never removes markers from already-visible text or tries to repair an
// invalid completion.
type harmonyDecoder struct {
	state     harmonyDecodeState
	buffer    string
	channel   string
	recipient string
	content   strings.Builder
	turn      provider.AssistantTurn
	tools     []provider.ToolSpec
}

func newHarmonyDecoder(tools []provider.ToolSpec) *harmonyDecoder {
	return &harmonyDecoder{tools: tools}
}

func (d *harmonyDecoder) Push(text string) ([]embedded.GenDelta, error) {
	if d.state == harmonyTerminal {
		if text == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: data followed a terminal marker", errMalformedHarmony)
	}
	d.buffer += text
	var deltas []embedded.GenDelta
	for {
		switch d.state {
		case harmonyHeader:
			index := strings.Index(d.buffer, harmonyMessage)
			if index < 0 {
				if len(d.buffer) > 4096 {
					return nil, fmt.Errorf("%w: message header exceeds limit", errMalformedHarmony)
				}
				return deltas, nil
			}
			header := d.buffer[:index]
			d.buffer = d.buffer[index+len(harmonyMessage):]
			channel, recipient, err := parseHarmonyHeader(header)
			if err != nil {
				return nil, err
			}
			d.channel = channel
			d.recipient = recipient
			d.content.Reset()
			d.state = harmonyContent
		case harmonyContent:
			marker, index := nextHarmonyTerminator(d.buffer)
			if index < 0 {
				hold := harmonyTerminatorSuffix(d.buffer)
				safe := d.buffer[:len(d.buffer)-hold]
				d.buffer = d.buffer[len(d.buffer)-hold:]
				deltas = append(deltas, d.routeContent(safe)...)
				return deltas, nil
			}
			deltas = append(deltas, d.routeContent(d.buffer[:index])...)
			d.buffer = d.buffer[index+len(marker):]
			if err := d.finishMessage(marker); err != nil {
				return nil, err
			}
			if d.state == harmonyTerminal {
				if d.buffer != "" {
					return nil, fmt.Errorf("%w: data followed a terminal marker", errMalformedHarmony)
				}
				return deltas, nil
			}
		}
	}
}

func (d *harmonyDecoder) routeContent(value string) []embedded.GenDelta {
	if value == "" {
		return nil
	}
	d.content.WriteString(value)
	switch d.channel {
	case "analysis":
		d.turn.Reasoning += value
		return []embedded.GenDelta{{Kind: embedded.DeltaReasoning, Text: value}}
	case "final":
		d.turn.FinalContent += value
		return []embedded.GenDelta{{Kind: embedded.DeltaText, Text: value}}
	default:
		// Commentary is either a tool-call payload or a user-safe preamble.
		// LLMTUI currently exposes only final as assistant answer content, so
		// it is retained semantically but not emitted into the transcript.
		return nil
	}
}

func (d *harmonyDecoder) finishMessage(marker string) error {
	switch marker {
	case harmonyEnd:
		if d.channel == "final" || d.recipient != "" {
			return fmt.Errorf("%w: invalid end marker for %s message", errMalformedHarmony, d.channel)
		}
		d.state = harmonyHeader
		return nil
	case harmonyReturn:
		if d.channel != "final" || d.recipient != "" {
			return fmt.Errorf("%w: return marker requires a final message", errMalformedHarmony)
		}
		d.turn.Completed = true
		d.state = harmonyTerminal
		return nil
	case harmonyCall:
		if d.channel != "commentary" || !strings.HasPrefix(d.recipient, "functions.") {
			return fmt.Errorf("%w: call marker requires a functions recipient on commentary", errMalformedHarmony)
		}
		name := strings.TrimPrefix(d.recipient, "functions.")
		call, err := d.normalizeCall(name, d.content.String())
		if err != nil {
			return err
		}
		d.turn.ToolCalls = []provider.ToolCall{call}
		if d.turn.Reasoning != "" {
			d.turn.Continuation = &provider.ProviderContinuation{Reasoning: d.turn.Reasoning}
		}
		d.state = harmonyTerminal
		return nil
	default:
		return fmt.Errorf("%w: unknown terminal marker", errMalformedHarmony)
	}
}

func (d *harmonyDecoder) normalizeCall(name, arguments string) (provider.ToolCall, error) {
	var values map[string]any
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil || values == nil {
		return provider.ToolCall{}, fmt.Errorf("%w: tool arguments are not a JSON object", errMalformedHarmony)
	}
	var spec *provider.ToolSpec
	for index := range d.tools {
		if d.tools[index].Name == name {
			spec = &d.tools[index]
			break
		}
	}
	if spec == nil {
		return provider.ToolCall{}, fmt.Errorf("%w: model requested an unknown tool", errMalformedHarmony)
	}
	schema, err := decodeJSONObject(spec.Parameters)
	if err != nil {
		return provider.ToolCall{}, fmt.Errorf("%w: offered tool schema is invalid", errMalformedHarmony)
	}
	argumentText := make(map[string]string, len(values))
	for key, value := range values {
		if text, ok := value.(string); ok {
			argumentText[key] = text
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return provider.ToolCall{}, fmt.Errorf("%w: tool argument cannot be encoded", errMalformedHarmony)
		}
		argumentText[key] = string(encoded)
	}
	if missing, err := missingRequiredArgument(schema, argumentText); err != nil {
		return provider.ToolCall{}, fmt.Errorf("%w: offered tool schema is invalid", errMalformedHarmony)
	} else if missing != "" {
		return provider.ToolCall{Name: name, ArgumentsError: fmt.Sprintf("call is missing required argument %q", missing)}, nil
	}
	for key, raw := range argumentText {
		if _, err := normalizeArgument(raw, propertySchema(schema, key)); err != nil {
			return provider.ToolCall{Name: name, ArgumentsError: fmt.Sprintf("argument %q is invalid: %v", key, err)}, nil
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return provider.ToolCall{}, fmt.Errorf("%w: tool arguments cannot be encoded", errMalformedHarmony)
	}
	return provider.ToolCall{Name: name, Arguments: string(encoded)}, nil
}

func (d *harmonyDecoder) Finish() (provider.AssistantTurn, error) {
	if d.state != harmonyTerminal || d.buffer != "" {
		return provider.AssistantTurn{}, fmt.Errorf("%w: completion ended before return or call", errMalformedHarmony)
	}
	return d.turn, nil
}

func parseHarmonyHeader(header string) (channel, recipient string, err error) {
	value := header
	if strings.HasPrefix(value, harmonyStart+"assistant") {
		value = strings.TrimPrefix(value, harmonyStart+"assistant")
	}
	index := strings.Index(value, harmonyChannel)
	if index < 0 {
		return "", "", fmt.Errorf("%w: assistant message has no channel", errMalformedHarmony)
	}
	metadata := value[:index] + " " + value[index+len(harmonyChannel):]
	channelFields := strings.Fields(value[index+len(harmonyChannel):])
	if len(channelFields) == 0 {
		return "", "", fmt.Errorf("%w: assistant channel is empty", errMalformedHarmony)
	}
	channel = channelFields[0]
	if channel != "analysis" && channel != "commentary" && channel != "final" {
		return "", "", fmt.Errorf("%w: unsupported assistant channel", errMalformedHarmony)
	}
	if start := strings.Index(metadata, "to=functions."); start >= 0 {
		target := metadata[start+len("to="):]
		if end := strings.IndexAny(target, " \t\r\n<"); end >= 0 {
			target = target[:end]
		}
		recipient = target
	}
	if recipient != "" && channel != "commentary" {
		return "", "", fmt.Errorf("%w: function recipient is not on commentary", errMalformedHarmony)
	}
	if strings.Contains(metadata, harmonyConstrain) && !strings.Contains(metadata, harmonyConstrain+"json") {
		return "", "", fmt.Errorf("%w: unsupported tool content constraint", errMalformedHarmony)
	}
	return channel, recipient, nil
}

func nextHarmonyTerminator(value string) (string, int) {
	marker := ""
	index := -1
	for _, candidate := range []string{harmonyEnd, harmonyReturn, harmonyCall} {
		if found := strings.Index(value, candidate); found >= 0 && (index < 0 || found < index) {
			marker, index = candidate, found
		}
	}
	return marker, index
}

func harmonyTerminatorSuffix(value string) int {
	hold := 0
	for _, marker := range []string{harmonyEnd, harmonyReturn, harmonyCall} {
		for size := min(len(value), len(marker)-1); size > hold; size-- {
			if strings.HasSuffix(value, marker[:size]) {
				hold = size
				break
			}
		}
	}
	return hold
}
