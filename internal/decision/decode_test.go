package decision

import (
	"math"
	"testing"
)

func TestDecodeLogitsChoiceAndCalibration(t *testing.T) {
	answer, err := DecodeLogits(Question{Type: QuestionChoice, Criteria: []string{"no", "yes"}}, []float64{1, 3}, DecodeOptions{Temperature: 0.1})
	if err != nil {
		t.Fatalf("DecodeLogits() error = %v", err)
	}
	if answer.Choice != "yes" || answer.Probabilities["yes"] <= answer.Probabilities["no"] {
		t.Fatalf("choice answer = %#v", answer)
	}
	if answer.Confidence < 0 || answer.Confidence > 1 {
		t.Fatalf("confidence = %v", answer.Confidence)
	}
}

func TestDecodeLogitsScoreAndNoul(t *testing.T) {
	score, err := DecodeLogits(Question{Type: QuestionScore, Criteria: []string{"low", "mid", "high"}}, []float64{0, 0, 0}, DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(score.Score-1) > 1e-9 {
		t.Fatalf("score = %v, want 1", score.Score)
	}
	noul, err := DecodeLogits(Question{Type: QuestionNoul}, []float64{0, 2}, DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if noul.Probability <= 0.5 || noul.Confidence < noul.Probability {
		t.Fatalf("noul answer = %#v", noul)
	}
}

func TestDecodeLogitsRejectsNonFiniteInput(t *testing.T) {
	if _, err := DecodeLogits(Question{Type: QuestionChoice, Criteria: []string{"a", "b"}}, []float64{math.NaN(), 1}, DecodeOptions{}); err == nil {
		t.Fatal("DecodeLogits() accepted NaN")
	}
}
