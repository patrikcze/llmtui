package decision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// TokenEncoder is the small tokenizer surface required by Laya sequence
// construction. A backend can wrap a verified tokenizer implementation without
// exposing its vocabulary or model-specific types to callers.
type TokenEncoder interface {
	CLSTokenID() int
	SEPTokenID() int
	MaskToken() string
	MaskTokenID() int
	Encode(text string) ([]int, error)
}

type SequenceOptions struct {
	MaxLen       int
	HeadMaxLen   int
	OptionOrder  []int
	TruncateLeft bool
}

type EncodedSequence struct {
	IDs             []int
	MarkerPositions []int
	QuestionType    QuestionType
}

// SerializeState follows Laya's input distinction: strings are already text,
// while structured state is JSON encoded without ASCII or HTML escaping.
func SerializeState(state any) (string, error) {
	if text, ok := state.(string); ok {
		return text, nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(state); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// RenderOptions converts typed criteria into the text alternatives consumed
// by Laya. Map criteria are sorted because Go maps do not preserve insertion
// order; callers needing a specific order should use []string for choices.
func RenderOptions(question Question) ([]string, error) {
	switch question.Type {
	case QuestionChoice:
		switch criteria := question.Criteria.(type) {
		case []string:
			if len(criteria) == 0 {
				return nil, fmt.Errorf("%w: choice criteria cannot be empty", ErrInvalid)
			}
			result := make([]string, len(criteria))
			for i, label := range criteria {
				if strings.TrimSpace(label) == "" {
					return nil, fmt.Errorf("%w: choice labels cannot be empty", ErrInvalid)
				}
				result[i] = label
			}
			return result, nil
		case map[string]string:
			keys := make([]string, 0, len(criteria))
			for key := range criteria {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			result := make([]string, 0, len(keys))
			for _, key := range keys {
				result = append(result, renderCriterionPair(key, criteria[key]))
			}
			return result, nil
		case map[string]any:
			keys := make([]string, 0, len(criteria))
			for key := range criteria {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			result := make([]string, 0, len(keys))
			for _, key := range keys {
				result = append(result, renderCriterionPair(key, criteria[key]))
			}
			return result, nil
		default:
			return nil, fmt.Errorf("%w: choice criteria must be a map or string list", ErrInvalid)
		}
	case QuestionScore:
		switch criteria := question.Criteria.(type) {
		case []string:
			result := make([]string, len(criteria))
			for i, value := range criteria {
				result[i] = fmt.Sprintf("level %d: %s", i, renderCriterion(value))
			}
			return result, nil
		case []any:
			result := make([]string, len(criteria))
			for i, value := range criteria {
				result[i] = fmt.Sprintf("level %d: %s", i, renderCriterion(value))
			}
			return result, nil
		default:
			return nil, fmt.Errorf("%w: score criteria must be an ordered list", ErrInvalid)
		}
	case QuestionNoul:
		falseValue, trueValue := any(nil), any(nil)
		switch criteria := question.Criteria.(type) {
		case map[string]string:
			falseValue, trueValue = criteria["false"], criteria["true"]
		case map[string]any:
			falseValue, trueValue = criteria["false"], criteria["true"]
		case nil:
		default:
			return nil, fmt.Errorf("%w: noul criteria must be a map", ErrInvalid)
		}
		return []string{"false: " + renderNoulCriterion(falseValue, "no, the statement does not hold"), "true: " + renderNoulCriterion(trueValue, "yes, the statement holds")}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported question type %q", ErrInvalid, question.Type)
	}
}

func renderCriterionPair(label string, value any) string {
	if strings.TrimSpace(label) == "" {
		return label
	}
	if value == nil || value == "" {
		return label
	}
	return label + ": " + renderCriterion(value)
}

func renderNoulCriterion(value any, fallback string) string {
	if value == nil || value == "" {
		return fallback
	}
	return renderCriterion(value)
}

func renderCriterion(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

// BuildSequence mirrors Laya's [CLS] head [SEP] masked options [SEP] state
// [SEP] layout, including the fixed option and state budgets.
func BuildSequence(tokenizer TokenEncoder, state any, question Question, options SequenceOptions) (EncodedSequence, error) {
	if tokenizer == nil {
		return EncodedSequence{}, fmt.Errorf("%w: tokenizer is required", ErrInvalid)
	}
	if err := question.validate("sequence"); err != nil {
		return EncodedSequence{}, err
	}
	maxLen := options.MaxLen
	if maxLen == 0 {
		maxLen = 512
	}
	headMaxLen := options.HeadMaxLen
	if headMaxLen == 0 {
		headMaxLen = 192
	}
	if maxLen < 2 || headMaxLen < 1 || headMaxLen > maxLen {
		return EncodedSequence{}, fmt.Errorf("%w: invalid sequence limits max_len=%d head_max_len=%d", ErrInvalid, maxLen, headMaxLen)
	}
	optionTexts, err := RenderOptions(question)
	if err != nil {
		return EncodedSequence{}, err
	}
	order := options.OptionOrder
	if len(order) == 0 {
		order = make([]int, len(optionTexts))
		for i := range order {
			order[i] = i
		}
	}
	if len(order) != len(optionTexts) {
		return EncodedSequence{}, fmt.Errorf("%w: option order length %d does not match option count %d", ErrInvalid, len(order), len(optionTexts))
	}
	seen := make(map[int]struct{}, len(order))
	for _, index := range order {
		if index < 0 || index >= len(optionTexts) {
			return EncodedSequence{}, fmt.Errorf("%w: option index %d is out of range", ErrInvalid, index)
		}
		if _, ok := seen[index]; ok {
			return EncodedSequence{}, fmt.Errorf("%w: option index %d is repeated", ErrInvalid, index)
		}
		seen[index] = struct{}{}
	}
	mask := tokenizer.MaskToken()
	head, err := tokenizer.Encode(question.Type.String() + " question: " + strings.ReplaceAll(question.Instructions, mask, " "))
	if err != nil {
		return EncodedSequence{}, fmt.Errorf("encode question head: %w", err)
	}
	optionIDs := make([][]int, 0, len(order))
	for _, index := range order {
		encoded, encodeErr := tokenizer.Encode(" " + strings.ReplaceAll(optionTexts[index], mask, " "))
		if encodeErr != nil {
			return EncodedSequence{}, fmt.Errorf("encode option %d: %w", index, encodeErr)
		}
		if len(encoded) > 48 {
			encoded = encoded[:48]
		}
		optionIDs = append(optionIDs, append([]int{tokenizer.MaskTokenID()}, encoded...))
	}
	optBudget := headMaxLen
	for _, ids := range optionIDs {
		optBudget -= len(ids)
	}
	if optBudget < 16 {
		per := max(4, (headMaxLen-16)/max(1, len(optionIDs)))
		for i := range optionIDs {
			if len(optionIDs[i]) > per {
				optionIDs[i] = optionIDs[i][:per]
			}
		}
		optBudget = headMaxLen
		for _, ids := range optionIDs {
			optBudget -= len(ids)
		}
	}
	if limit := max(8, optBudget); len(head) > limit {
		head = head[:limit]
	}
	ids := []int{tokenizer.CLSTokenID()}
	ids = append(ids, head...)
	ids = append(ids, tokenizer.SEPTokenID())
	markers := make([]int, 0, len(optionIDs))
	for _, option := range optionIDs {
		markers = append(markers, len(ids))
		ids = append(ids, option...)
	}
	ids = append(ids, tokenizer.SEPTokenID())
	room := max(0, maxLen-len(ids)-1)
	stateText, err := SerializeState(state)
	if err != nil {
		return EncodedSequence{}, fmt.Errorf("serialize state: %w", err)
	}
	stateIDs, err := tokenizer.Encode(strings.ReplaceAll(stateText, mask, " "))
	if err != nil {
		return EncodedSequence{}, fmt.Errorf("encode state: %w", err)
	}
	if len(stateIDs) > room {
		if options.TruncateLeft {
			stateIDs = stateIDs[len(stateIDs)-room:]
		} else {
			stateIDs = stateIDs[:room]
		}
	}
	ids = append(ids, stateIDs...)
	ids = append(ids, tokenizer.SEPTokenID())
	if len(ids) > maxLen {
		ids = ids[:maxLen]
	}
	filteredMarkers := markers[:0]
	for _, marker := range markers {
		if marker < maxLen {
			filteredMarkers = append(filteredMarkers, marker)
		}
	}
	return EncodedSequence{IDs: ids, MarkerPositions: filteredMarkers, QuestionType: question.Type}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (t QuestionType) String() string { return string(t) }
