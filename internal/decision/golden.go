package decision

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const GoldenFixtureSchema = 1

// GoldenFixture is the interchange contract between a pinned Python
// reference run and a Go runtime. It deliberately stores structured inputs
// and normalized answers, rather than backend-specific token IDs.
type GoldenFixture struct {
	SchemaVersion    int          `json:"schema_version"`
	UpstreamRevision string       `json:"upstream_revision"`
	Cases            []GoldenCase `json:"cases"`
}

type GoldenCase struct {
	Name      string              `json:"name"`
	State     json.RawMessage     `json:"state,omitempty"`
	Questions map[string]Question `json:"questions"`
	Expected  Result              `json:"expected"`
}

// ValidateGoldenFixture checks the stable boundary used by future exporter
// and runtime tests. It prevents an incomplete fixture from becoming a parity
// claim, while leaving tokenization and model-specific details to the runner.
func ValidateGoldenFixture(fixture GoldenFixture) error {
	if fixture.SchemaVersion != GoldenFixtureSchema {
		return fmt.Errorf("%w: unsupported golden fixture schema %d", ErrInvalid, fixture.SchemaVersion)
	}
	if strings.TrimSpace(fixture.UpstreamRevision) == "" {
		return fmt.Errorf("%w: golden fixture upstream revision is required", ErrInvalid)
	}
	if len(fixture.Cases) == 0 || len(fixture.Cases) > 256 {
		return fmt.Errorf("%w: golden fixture must contain 1-256 cases", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(fixture.Cases))
	for i, golden := range fixture.Cases {
		name := strings.TrimSpace(golden.Name)
		if name == "" || len(name) > 128 {
			return fmt.Errorf("%w: golden case %d has an invalid name", ErrInvalid, i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: duplicate golden case %q", ErrInvalid, name)
		}
		seen[name] = struct{}{}
		state, err := decodeFixtureState(golden.State)
		if err != nil {
			return fmt.Errorf("%w: golden case %q state: %v", ErrInvalid, name, err)
		}
		if err := Validate(state, golden.Questions); err != nil {
			return fmt.Errorf("%w: golden case %q input: %v", ErrInvalid, name, err)
		}
		if len(golden.Expected.Answers) != len(golden.Questions) {
			return fmt.Errorf("%w: golden case %q answer count does not match questions", ErrInvalid, name)
		}
		ids := make([]string, 0, len(golden.Questions))
		for id := range golden.Questions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			answer, ok := golden.Expected.Answers[id]
			if !ok {
				return fmt.Errorf("%w: golden case %q is missing answer %q", ErrInvalid, name, id)
			}
			if answer.Type != golden.Questions[id].Type {
				return fmt.Errorf("%w: golden case %q answer %q type mismatch", ErrInvalid, name, id)
			}
			if err := ValidateAnswer(answer); err != nil {
				return fmt.Errorf("%w: golden case %q answer %q: %v", ErrInvalid, name, id, err)
			}
		}
	}
	return nil
}

func decodeFixtureState(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var state any
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	return state, nil
}
