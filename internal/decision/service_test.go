package decision

import (
	"context"
	"errors"
	"testing"
)

type serviceEngine struct {
	result Result
}

func (serviceEngine) Name() string { return "service-test" }
func (e serviceEngine) Predict(context.Context, any, map[string]Question, PredictOptions) (Result, error) {
	return e.result, nil
}
func (serviceEngine) Close() error { return nil }

func TestServiceDisabledReturnsUnavailable(t *testing.T) {
	service := NewService(false, serviceEngine{})
	_, err := service.Predict(context.Background(), nil, map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"a", "b"}}}, PredictOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled Predict() error = %v, want ErrUnavailable", err)
	}
}

func TestServiceValidatesBackendResult(t *testing.T) {
	questions := map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"a", "b"}}}
	service := NewService(true, serviceEngine{result: Result{Answers: map[string]Answer{
		"q": {Type: QuestionChoice, Choice: "a", Confidence: 0.9},
	}}})
	result, err := service.Predict(context.Background(), nil, questions, PredictOptions{})
	if err != nil || result.Answers["q"].Choice != "a" {
		t.Fatalf("valid Predict() = %#v, error %v", result, err)
	}

	service = NewService(true, serviceEngine{result: Result{Answers: map[string]Answer{
		"q": {Type: QuestionScore, Score: 1},
	}}})
	if _, err := service.Predict(context.Background(), nil, questions, PredictOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid result error = %v, want ErrInvalid", err)
	}
}
