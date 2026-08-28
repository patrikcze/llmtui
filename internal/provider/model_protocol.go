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

// HarmonyContentGuard validates provider-managed visible content while
// preserving streaming. It retains only suffixes that could become a split
// control token. It never edits or repairs a response: any complete protocol
// marker fails the stream.
type HarmonyContentGuard struct {
	pending strings.Builder
	decided bool
}

func (g *HarmonyContentGuard) Feed(delta string) (string, error) {
	g.pending.WriteString(delta)
	value := g.pending.String()
	if !g.decided {
		trimmed := strings.TrimLeft(value, " \t\r\n")
		for _, prefix := range []string{"to=functions.", "to=functions ", "to=...?"} {
			if strings.HasPrefix(prefix, trimmed) {
				return "", nil
			}
			if strings.HasPrefix(trimmed, prefix) {
				return "", ErrHarmonyProtocol
			}
		}
		g.decided = true
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
	trimmed := strings.TrimSpace(value)
	if ContainsHarmonyControlToken(value) || strings.HasPrefix(trimmed, "to=functions.") ||
		strings.HasPrefix(trimmed, "to=functions ") || trimmed == "to=...?" {
		return "", ErrHarmonyProtocol
	}
	return value, nil
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
