package provider

import (
	"errors"
	"strings"
)

// ModelFamily is a centralized model protocol identity. Provider adapters use
// this instead of scattering model-name substring checks.
type ModelFamily uint8

const (
	ModelFamilyUnknown ModelFamily = iota
	ModelFamilyGPTOSS
)

// TemplateOwnership identifies the single layer responsible for rendering a
// model-specific chat protocol.
type TemplateOwnership uint8

const (
	TemplateOwnershipUnknown TemplateOwnership = iota
	TemplateOwnershipProvider
	TemplateOwnershipEmbeddedRuntime
)

// ModelProtocol describes semantic behavior that is independent of a
// provider's transport shape.
type ModelProtocol struct {
	Family                        ModelFamily
	HarmonyRequired               bool
	ReasoningSupported            bool
	ReasoningContinuationRequired bool
	NativeToolsSupported          bool
	DefaultReasoningEffort        string
}

// ResolveModelProtocol prefers architecture metadata, then uses a documented
// segment-based model-ID fallback for transports that expose only a name.
func ResolveModelProtocol(modelID, architecture string) ModelProtocol {
	if modelFamily(architecture) == ModelFamilyGPTOSS || modelFamily(modelID) == ModelFamilyGPTOSS {
		return ModelProtocol{
			Family:                        ModelFamilyGPTOSS,
			HarmonyRequired:               true,
			ReasoningSupported:            true,
			ReasoningContinuationRequired: true,
			NativeToolsSupported:          true,
			DefaultReasoningEffort:        "medium",
		}
	}
	return ModelProtocol{}
}

func modelFamily(value string) ModelFamily {
	normalized := strings.Trim(strings.ToLower(value), " \t\r\n")
	for start := 0; start < len(normalized); start++ {
		if !strings.HasPrefix(normalized[start:], "gpt-oss") {
			continue
		}
		end := start + len("gpt-oss")
		beforeOK := start == 0 || !asciiLetter(normalized[start-1])
		afterOK := end == len(normalized) || !asciiLetter(normalized[end])
		if beforeOK && afterOK {
			return ModelFamilyGPTOSS
		}
	}
	return ModelFamilyUnknown
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z'
}

// ReasoningEffort resolves the configured value for a model. GPT-OSS cannot
// disable reasoning; auto/on select its documented medium default.
func ReasoningEffort(protocol ModelProtocol, configured string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(configured))
	if protocol.Family != ModelFamilyGPTOSS {
		return "", false
	}
	switch value {
	case "", "auto", "on":
		return protocol.DefaultReasoningEffort, true
	case "low", "medium", "high":
		return value, true
	default:
		return "", false
	}
}

// ContainsHarmonyControlToken reports a provider protocol violation. It is a
// validator, never an output-repair mechanism: callers must fail the response
// rather than remove these tokens.
func ContainsHarmonyControlToken(content string) bool {
	for _, marker := range harmonyControlTokens {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

var (
	// ErrHarmonyProtocol is returned when a provider declared structured
	// GPT-OSS output but exposed raw Harmony wire syntax as visible content.
	ErrHarmonyProtocol   = errors.New("provider exposed malformed or unparsed Harmony output")
	harmonyControlTokens = []string{
		"<|start|>", "<|end|>", "<|message|>", "<|channel|>",
		"<|constrain|>", "<|return|>", "<|call|>",
	}
)

// harmonyRecipientMarker is Harmony's recipient-directive token, e.g.
// "to=functions.change_apply". Grammar, not convention, restricts it to the
// first bytes of a message segment: OpenAI/Ollama both stream reasoning on
// their own field (Delta.Reasoning/ReasoningContent, Message.Thinking), so
// visible content ever reaching HarmonyContentGuard carries only final-
// channel text or a leaked non-final segment, and a leaked segment always
// opens with its recipient header. A message-initial "to=" is therefore
// always a leaked or corrupted header, never model-facing prose, regardless
// of what identifier or corruption follows the marker itself — which is why
// the guard checks for the marker, not for a catalog of corrupted forms it
// has previously happened to see (a specific corrupted continuation is not
// the protocol violation; the marker appearing in visible content is).
const harmonyRecipientMarker = "to="

// HarmonyContentGuard validates provider-managed visible content while
// preserving streaming. It retains only suffixes that could become a split
// control token, or a split recipient marker at message start. It never
// edits or repairs a response: any complete protocol marker, or a leaked
// recipient header, fails the stream.
type HarmonyContentGuard struct {
	pending strings.Builder
	decided bool
}

func (g *HarmonyContentGuard) Feed(delta string) (string, error) {
	g.pending.WriteString(delta)
	value := g.pending.String()
	if !g.decided {
		switch decision := matchHarmonyRecipientMarker(value); decision {
		case harmonyMarkerPending:
			return "", nil
		case harmonyMarkerFound:
			return "", ErrHarmonyProtocol
		case harmonyMarkerAbsent:
			g.decided = true
		}
	}
	if ContainsHarmonyControlToken(value) {
		return "", ErrHarmonyProtocol
	}
	hold := harmonyMarkerSuffix(value)
	emit := value[:len(value)-hold]
	g.pending.Reset()
	g.pending.WriteString(value[len(value)-hold:])
	return emit, nil
}

func (g *HarmonyContentGuard) Finish() (string, error) {
	value := g.pending.String()
	g.pending.Reset()
	if ContainsHarmonyControlToken(value) {
		return "", ErrHarmonyProtocol
	}
	// A still-ambiguous prefix (e.g. a message that is only ever "to") never
	// gets to resolve once the stream ends; treat it as ordinary content,
	// not as a confirmed marker. Only a complete match fails here.
	if !g.decided && matchHarmonyRecipientMarker(value) == harmonyMarkerFound {
		return "", ErrHarmonyProtocol
	}
	return value, nil
}

type harmonyMarkerDecision uint8

const (
	// harmonyMarkerAbsent means the buffered content is long enough to prove
	// it cannot become the recipient marker at this position.
	harmonyMarkerAbsent harmonyMarkerDecision = iota
	// harmonyMarkerPending means the buffered content is a prefix of the
	// marker so far; more bytes are needed to decide.
	harmonyMarkerPending
	// harmonyMarkerFound means the buffered content contains the complete
	// marker at message start.
	harmonyMarkerFound
)

// matchHarmonyRecipientMarker checks whether value, once leading whitespace
// is trimmed, opens with harmonyRecipientMarker. Leading whitespace is
// trimmed only for the comparison; the caller still emits it unchanged.
func matchHarmonyRecipientMarker(value string) harmonyMarkerDecision {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, harmonyRecipientMarker):
		return harmonyMarkerFound
	case strings.HasPrefix(harmonyRecipientMarker, trimmed):
		return harmonyMarkerPending
	default:
		return harmonyMarkerAbsent
	}
}

func harmonyMarkerSuffix(value string) int {
	hold := 0
	for _, marker := range harmonyControlTokens {
		limit := min(len(value), len(marker)-1)
		for size := limit; size > hold; size-- {
			if strings.HasSuffix(value, marker[:size]) {
				hold = size
				break
			}
		}
	}
	return hold
}
