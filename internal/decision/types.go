package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// QuestionType identifies a typed Laya-style question without coupling callers
// to a particular model implementation.
type QuestionType string

const (
	QuestionChoice QuestionType = "choice"
	QuestionScore  QuestionType = "score"
	QuestionNoul   QuestionType = "noul"
)

// Question describes one structured decision. Criteria is intentionally an
// interface because Laya accepts a map for choice, an ordered list for score,
// and an optional false/true map for noul.
type Question struct {
	Type         QuestionType `json:"type" yaml:"type"`
	Instructions string       `json:"instructions" yaml:"instructions"`
	Criteria     any          `json:"criteria,omitempty" yaml:"criteria,omitempty"`
}

// PredictOptions controls one prediction without changing engine state.
type PredictOptions struct {
	Model string
	// RequireCompleteInput requests strict capacity admission: a backend
	// that can measure exact token usage against its pinned tokenizer must
	// reject the request, before running inference, if any question's head,
	// options, or state would be silently truncated to fit the budget —
	// rather than silently proceeding on incomplete input. Default false
	// preserves existing best-effort behavior; existing callers and
	// backends that cannot measure capacity are unaffected. A backend that
	// cannot honor this (no tokenizer boundary to measure against) must
	// leave Result.InputUsage empty rather than fabricate zero-loss usage.
	RequireCompleteInput bool
}

// QuestionInputUsage reports exact token-budget accounting for one
// question's prepared input, computed by a backend using its own pinned
// tokenizer and upstream sequence-construction rules — never estimated
// from Go byte counts, and never a second, independent tokenizer. No
// question/option/state text is ever included, only counts and flags. A
// missing entry for a question key means the backend could not measure
// that question's usage — callers must treat that as unknown, never as a
// confident zero-truncation result.
type QuestionInputUsage struct {
	// HeadTokens/OptionTokens/StateTokens are the token counts actually
	// admitted into the model's input for that segment — i.e. after any
	// truncation was applied, exactly what real inference saw.
	HeadTokens   int `json:"head_tokens"`
	OptionTokens int `json:"option_tokens"`
	StateTokens  int `json:"state_tokens"`
	// MaxLen is the total sequence token budget; StateBudget is the room
	// left for state after the head/options prefix and framing tokens.
	MaxLen      int `json:"max_len"`
	StateBudget int `json:"state_budget"`
	// HeadTruncated/OptionsTruncated/StateTruncated report whether that
	// segment's raw (untruncated) content exceeded what was admitted —
	// i.e. whether any of the caller's supplied content was silently lost.
	HeadTruncated    bool `json:"head_truncated"`
	OptionsTruncated bool `json:"options_truncated"`
	StateTruncated   bool `json:"state_truncated"`
}

// Truncated reports whether any segment of this question's input lost
// content to the tokenizer's budget.
func (u QuestionInputUsage) Truncated() bool {
	return u.HeadTruncated || u.OptionsTruncated || u.StateTruncated
}

// Answer is the normalized representation shared by future decision engines.
// Only the field matching Type is meaningful. Probabilities and Confidence
// must be finite and lie in [0,1] when supplied.
type Answer struct {
	Type          QuestionType       `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Probability   float64            `json:"probability,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// Routing describes how an engine selected its model. It is diagnostic data,
// not a policy decision and must not be used to bypass an approval gate.
type Routing struct {
	Model      string `json:"model,omitempty"`
	Repository string `json:"repo,omitempty"`
	// Revision is the exact installed manifest revision the Router actually
	// loaded and is using for this prediction — bound once at load time,
	// never re-derived from a later catalog/installation query, so it
	// cannot silently drift if a newer installation appears while the
	// loaded engine is still in use. Empty when the backend/store does not
	// track installation revisions.
	Revision     string `json:"revision,omitempty"`
	Reason       string `json:"reason,omitempty"`
	DetectedLang string `json:"detected_language,omitempty"`
}

// Result is the engine-independent prediction result.
type Result struct {
	Answers map[string]Answer `json:"answers"`
	Routing Routing           `json:"routing,omitempty"`
	// InputUsage carries per-question token-capacity diagnostics when the
	// backend supports computing them (currently MLX only). Additive and
	// empty-safe: a nil/empty map means the backend did not measure usage,
	// not that no question was truncated.
	InputUsage map[string]QuestionInputUsage `json:"input_usage,omitempty"`
}

// Engine is the deliberately small boundary between structured decisions and
// the generative provider stack. Implementations must be safe for the
// documented concurrency level and must honor ctx cancellation.
type Engine interface {
	Name() string
	Predict(ctx context.Context, state any, questions map[string]Question, opts PredictOptions) (Result, error)
	Close() error
}

var (
	ErrUnavailable = errors.New("decision engine unavailable")
	ErrInvalid     = errors.New("invalid decision request")
)

// Validate checks the bounded, provider-independent request contract before a
// backend is selected. JSON marshaling is used to preserve Laya's state
// serialization semantics and reject values that cannot be represented.
func Validate(state any, questions map[string]Question) error {
	if err := validateState(state); err != nil {
		return err
	}
	if len(questions) == 0 {
		return fmt.Errorf("%w: at least one question is required", ErrInvalid)
	}
	if len(questions) > 128 {
		return fmt.Errorf("%w: question count %d exceeds 128", ErrInvalid, len(questions))
	}
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > 128 {
			return fmt.Errorf("%w: question id must be 1-128 non-space bytes", ErrInvalid)
		}
		if err := questions[id].validate(id); err != nil {
			return err
		}
	}
	return nil
}

func validateState(state any) error {
	if state == nil {
		return nil
	}
	b, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: serialize state: %v", ErrInvalid, err)
	}
	if len(b) > 4<<20 {
		return fmt.Errorf("%w: serialized state exceeds 4 MiB", ErrInvalid)
	}
	return nil
}

func (q Question) validate(id string) error {
	if q.Type != QuestionChoice && q.Type != QuestionScore && q.Type != QuestionNoul {
		return fmt.Errorf("%w: question %q has unsupported type %q", ErrInvalid, id, q.Type)
	}
	if len(q.Instructions) > 16<<10 {
		return fmt.Errorf("%w: question %q instructions exceed 16 KiB", ErrInvalid, id)
	}
	if err := validateJSON(q.Criteria); err != nil {
		return fmt.Errorf("%w: question %q criteria: %v", ErrInvalid, id, err)
	}
	switch q.Type {
	case QuestionChoice:
		if err := validateChoiceCriteria(q.Criteria); err != nil {
			return fmt.Errorf("%w: question %q: %v", ErrInvalid, id, err)
		}
	case QuestionScore:
		if err := validateScoreCriteria(q.Criteria); err != nil {
			return fmt.Errorf("%w: question %q: %v", ErrInvalid, id, err)
		}
	}
	return nil
}

func validateJSON(value any) error {
	if value == nil {
		return nil
	}
	if _, err := json.Marshal(value); err != nil {
		return err
	}
	return nil
}

func validateChoiceCriteria(value any) error {
	switch criteria := value.(type) {
	case map[string]string:
		if len(criteria) == 0 {
			return errors.New("choice criteria cannot be empty")
		}
		for label := range criteria {
			if strings.TrimSpace(label) == "" {
				return errors.New("choice labels cannot be empty")
			}
		}
	case map[string]any:
		if len(criteria) == 0 {
			return errors.New("choice criteria cannot be empty")
		}
		for label := range criteria {
			if strings.TrimSpace(label) == "" {
				return errors.New("choice labels cannot be empty")
			}
		}
	case []string:
		if len(criteria) == 0 {
			return errors.New("choice criteria cannot be empty")
		}
		for _, label := range criteria {
			if strings.TrimSpace(label) == "" {
				return errors.New("choice labels cannot be empty")
			}
		}
	default:
		return errors.New("choice criteria must be a map or string list")
	}
	return nil
}

func validateScoreCriteria(value any) error {
	switch criteria := value.(type) {
	case []string:
		if len(criteria) == 0 {
			return errors.New("score criteria cannot be empty")
		}
	case []any:
		if len(criteria) == 0 {
			return errors.New("score criteria cannot be empty")
		}
	default:
		return errors.New("score criteria must be an ordered list")
	}
	return nil
}

// ValidateAnswer is useful at the engine boundary and in tests. It rejects
// NaN/Inf and out-of-range confidence data before it can influence policy.
func ValidateAnswer(answer Answer) error {
	if answer.Type != QuestionChoice && answer.Type != QuestionScore && answer.Type != QuestionNoul {
		return fmt.Errorf("%w: unsupported answer type %q", ErrInvalid, answer.Type)
	}
	for name, value := range map[string]float64{
		"probability": valueOrZero(answer.Probability),
		"confidence":  valueOrZero(answer.Confidence),
	} {
		if value != 0 && (math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1) {
			return fmt.Errorf("%w: %s must be between 0 and 1", ErrInvalid, name)
		}
	}
	if math.IsNaN(answer.Score) || math.IsInf(answer.Score, 0) {
		return fmt.Errorf("%w: score must be finite", ErrInvalid)
	}
	for label, value := range answer.Probabilities {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return fmt.Errorf("%w: probability for %q must be between 0 and 1", ErrInvalid, label)
		}
	}
	return nil
}

func valueOrZero(value float64) float64 { return value }

// UnavailableEngine is an explicit safe fallback while a runtime backend is
// unavailable. Callers should preserve the existing generative path on this
// error; it never fabricates a classification.
type UnavailableEngine struct{ Reason string }

func (UnavailableEngine) Name() string { return "unavailable" }

func (e UnavailableEngine) Predict(ctx context.Context, state any, questions map[string]Question, opts PredictOptions) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := Validate(state, questions); err != nil {
		return Result{}, err
	}
	if e.Reason == "" {
		return Result{}, ErrUnavailable
	}
	return Result{}, fmt.Errorf("%w: %s", ErrUnavailable, e.Reason)
}

func (UnavailableEngine) Close() error { return nil }
