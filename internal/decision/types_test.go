package decision

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestValidateQuestions(t *testing.T) {
	tests := []struct {
		name      string
		questions map[string]Question
		wantErr   bool
	}{
		{name: "choice map", questions: map[string]Question{"route": {Type: QuestionChoice, Criteria: map[string]string{"a": "A"}}}},
		{name: "choice list", questions: map[string]Question{"route": {Type: QuestionChoice, Criteria: []string{"a", "b"}}}},
		{name: "score", questions: map[string]Question{"urgency": {Type: QuestionScore, Criteria: []string{"low", "high"}}}},
		{name: "noul optional criteria", questions: map[string]Question{"risk": {Type: QuestionNoul}}},
		{name: "unknown type", questions: map[string]Question{"x": {Type: "other"}}, wantErr: true},
		{name: "empty choice", questions: map[string]Question{"x": {Type: QuestionChoice, Criteria: map[string]string{}}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(map[string]string{"body": "hello"}, tt.questions)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsUnrepresentableAndOversizedState(t *testing.T) {
	if err := Validate(func() {}, map[string]Question{"q": {Type: QuestionNoul}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("function state error = %v, want ErrInvalid", err)
	}
	if err := Validate(string(make([]byte, 4<<20)), map[string]Question{"q": {Type: QuestionNoul}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("large state error = %v, want ErrInvalid", err)
	}
}

func TestValidateAnswerRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), -0.1, 1.1} {
		if err := ValidateAnswer(Answer{Type: QuestionNoul, Confidence: value}); !errors.Is(err, ErrInvalid) {
			t.Errorf("confidence %v error = %v, want ErrInvalid", value, err)
		}
	}
}

func TestUnavailableEngineNeverFabricatesAnswers(t *testing.T) {
	got, err := (UnavailableEngine{Reason: "no verified runtime artifact"}).Predict(context.Background(), "hello", map[string]Question{
		"risk": {Type: QuestionNoul},
	}, PredictOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Predict() error = %v, want ErrUnavailable", err)
	}
	if got.Answers != nil {
		t.Fatalf("Predict() answers = %#v, want nil", got.Answers)
	}
}
