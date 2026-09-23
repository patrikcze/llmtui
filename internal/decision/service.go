package decision

import (
	"context"
	"fmt"
)

// Service is the conservative integration point for callers that may opt in
// to structured decisions. Disabled services preserve the existing caller
// path by returning an explicit unavailable error and never fabricating data.
type Service struct {
	enabled bool
	engine  Engine
}

func NewService(enabled bool, engine Engine) *Service {
	return &Service{enabled: enabled, engine: engine}
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }

func (s *Service) Predict(ctx context.Context, state any, questions map[string]Question, options PredictOptions) (Result, error) {
	if err := Validate(state, questions); err != nil {
		return Result{}, err
	}
	if s == nil || !s.enabled || s.engine == nil {
		return Result{}, ErrUnavailable
	}
	result, err := s.engine.Predict(ctx, state, questions, options)
	if err != nil {
		return Result{}, err
	}
	if err := ValidateResult(questions, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) Close() error {
	if s == nil || s.engine == nil {
		return nil
	}
	return s.engine.Close()
}

// ValidateResult prevents a backend from silently omitting, inventing, or
// changing the type of a requested answer before a caller evaluates it.
func ValidateResult(questions map[string]Question, result Result) error {
	if len(result.Answers) != len(questions) {
		return fmt.Errorf("%w: result has %d answers for %d questions", ErrInvalid, len(result.Answers), len(questions))
	}
	for id, question := range questions {
		answer, ok := result.Answers[id]
		if !ok {
			return fmt.Errorf("%w: result is missing answer %q", ErrInvalid, id)
		}
		if answer.Type != question.Type {
			return fmt.Errorf("%w: answer %q type %q does not match question type %q", ErrInvalid, id, answer.Type, question.Type)
		}
		if err := ValidateAnswer(answer); err != nil {
			return fmt.Errorf("%w: answer %q: %v", ErrInvalid, id, err)
		}
	}
	return nil
}
