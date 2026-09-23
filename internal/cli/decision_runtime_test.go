package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadDecisionRequest(t *testing.T) {
	input := `{"state":{"n":9007199254740993},"questions":{"q":{"type":"choice","criteria":["yes","no"]}}}`
	state, questions, err := readDecisionRequest(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(state.(json.RawMessage)) != `{"n":9007199254740993}` {
		t.Fatalf("state lost precision: %s", state)
	}
	if _, ok := questions["q"].Criteria.([]string); !ok {
		t.Fatal("choice list not normalized")
	}
	for _, bad := range []string{`{} {}`, `{"questions":{"q":{"type":"choice","criteria":[1]}}}`, `{"questions":{}}`} {
		if _, _, err := readDecisionRequest(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
