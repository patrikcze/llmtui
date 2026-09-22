package decision

import (
	"reflect"
	"testing"
)

type countingTokenizer struct{}

func (countingTokenizer) CLSTokenID() int   { return 101 }
func (countingTokenizer) SEPTokenID() int   { return 102 }
func (countingTokenizer) MaskToken() string { return "[MASK]" }
func (countingTokenizer) MaskTokenID() int  { return 103 }
func (countingTokenizer) Encode(text string) ([]int, error) {
	words := splitWords(text)
	ids := make([]int, len(words))
	for i := range ids {
		ids[i] = i + 200
	}
	return ids, nil
}

func splitWords(text string) []string {
	var result []string
	var current []rune
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' {
			if len(current) > 0 {
				result = append(result, string(current))
				current = nil
			}
			continue
		}
		current = append(current, r)
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}

func TestRenderOptions(t *testing.T) {
	choice, err := RenderOptions(Question{Type: QuestionChoice, Criteria: map[string]string{"b": "two", "a": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b: two"}; !reflect.DeepEqual(choice, want) {
		t.Fatalf("choice options = %#v, want %#v", choice, want)
	}
	noul, err := RenderOptions(Question{Type: QuestionNoul})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"false: no, the statement does not hold", "true: yes, the statement holds"}; !reflect.DeepEqual(noul, want) {
		t.Fatalf("noul options = %#v, want %#v", noul, want)
	}
}

func TestBuildSequencePreservesMarkersAndTruncatesState(t *testing.T) {
	sequence, err := BuildSequence(countingTokenizer{}, "one two three four", Question{
		Type:         QuestionChoice,
		Instructions: "do thing",
		Criteria:     []string{"yes", "no"},
	}, SequenceOptions{MaxLen: 14, HeadMaxLen: 10, OptionOrder: []int{1, 0}})
	if err != nil {
		t.Fatalf("BuildSequence() error = %v", err)
	}
	if want := []int{101, 200, 201, 202, 203, 102, 103, 200, 103, 200, 102, 200, 201, 102}; !reflect.DeepEqual(sequence.IDs, want) {
		t.Fatalf("sequence IDs = %#v, want %#v", sequence.IDs, want)
	}
	if want := []int{6, 8}; !reflect.DeepEqual(sequence.MarkerPositions, want) {
		t.Fatalf("marker positions = %#v, want %#v", sequence.MarkerPositions, want)
	}
}

func TestBuildSequenceRejectsInvalidOptionOrder(t *testing.T) {
	_, err := BuildSequence(countingTokenizer{}, nil, Question{
		Type: QuestionChoice, Criteria: []string{"yes", "no"},
	}, SequenceOptions{OptionOrder: []int{0, 0}})
	if err == nil {
		t.Fatal("BuildSequence() accepted repeated option order")
	}
}
