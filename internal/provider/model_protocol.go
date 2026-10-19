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
	// ModelFamilyQwen38 is Qwen 3.8, whose chat template reads its own
	// reasoning_effort levels (low, medium, xhigh) beside enable_thinking.
	ModelFamilyQwen38
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
	if modelFamily(architecture) == ModelFamilyQwen38 || modelFamily(modelID) == ModelFamilyQwen38 {
		// No DefaultReasoningEffort: auto/on leave the template's own default
		// (xhigh) untouched.
		return ModelProtocol{Family: ModelFamilyQwen38, ReasoningSupported: true}
	}
	return ModelProtocol{}
}

func modelFamily(value string) ModelFamily {
	normalized := strings.Trim(strings.ToLower(value), " \t\r\n")
	if MatchesWordBoundary(normalized, "gpt-oss") {
		return ModelFamilyGPTOSS
	}
	if isQwen38(normalized) {
		return ModelFamilyQwen38
	}
	return ModelFamilyUnknown
}

// isQwen38 matches Qwen 3.8 IDs as servers and files name them —
// "qwen3.8-27b", "qwen/qwen3.8-27b", "qwen3.8:27b",
// "mlx-community/qwen3_8-27b-4bit", "qwen-3.8" — but not a longer version
// such as "qwen3.80", nor "qwen3_8b", which is Qwen3's 8B size (the iOS
// app's qwen-?3[._]8(?![0-9]), without the lookahead RE2 lacks, plus that
// size guard). value must already be lowercase.
func isQwen38(value string) bool {
	for idx := 0; ; {
		i := strings.Index(value[idx:], "qwen")
		if i < 0 {
			return false
		}
		rest := value[idx+i+len("qwen"):]
		rest = strings.TrimPrefix(rest, "-")
		if len(rest) >= 3 && rest[0] == '3' && (rest[1] == '.' || rest[1] == '_') && rest[2] == '8' &&
			(len(rest) == 3 || (rest[3] != 'b' && (rest[3] < '0' || rest[3] > '9'))) {
			return true
		}
		idx += i + 1
	}
}

// MatchesWordBoundary reports whether pattern occurs in text with no ASCII
// letter directly attached on either side. Plain strings.Contains would let
// "coder" match "encoder", or "gpt-oss" match a hypothetical "gpt-osso",
// silently misrouting an unrelated model. Digits are deliberately allowed to
// sit directly against the pattern, since model IDs routinely attach a
// version number with no separator ("qwen3", "llama3.1", "gpt-oss20b").
//
// Shared by this package's own narrow, safety-critical family detection
// (ModelFamilyGPTOSS, gating Harmony leak detection) and
// internal/modelprofile's broader per-family tuning-default matcher
// (context window, temperature, prompt style, six-plus families) — two
// independently scoped matchers with different callers and different
// stakes that happen to need the same boundary algorithm. Do not fold
// modelprofile's family list or this package's Harmony protocol handling
// into one package: that would couple a cosmetic-defaults concern to a
// leak-detection safety boundary for no benefit.
func MatchesWordBoundary(text, pattern string) bool {
	idx := 0
	for {
		i := strings.Index(text[idx:], pattern)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(pattern)
		beforeOK := start == 0 || !asciiLetter(text[start-1])
		afterOK := end == len(text) || !asciiLetter(text[end])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z'
}

// ReasoningEffort resolves the configured value for a model. GPT-OSS cannot
// disable reasoning; auto/on select its documented medium default. Qwen 3.8
// takes low, medium, or high, with high sent as the template's xhigh (its
// template raises an exception for "high"); auto/on/off carry no effort.
// Every other family has no effort control.
func ReasoningEffort(protocol ModelProtocol, configured string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(configured))
	if protocol.Family == ModelFamilyQwen38 {
		switch value {
		case "low", "medium":
			return value, true
		case "high", "xhigh":
			return "xhigh", true
		default:
			return "", false
		}
	}
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
