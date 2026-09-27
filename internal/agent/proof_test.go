package agent

import "testing"

func TestRequestNamesUnaddressedMutation(t *testing.T) {
	one := []Criterion{{ID: "c1", Text: "Read the file report.md"}}
	tests := []struct {
		name     string
		request  string
		criteria []Criterion
		want     bool
	}{
		{"plain write", "read report.md and write its heading to result.txt", one, true},
		{"inflected", "Writing the summary into notes.md", one, true},
		{"past tense", "make sure the file is saved", one, true},
		{"e-dropping ing", "try creating a backup", one, true},
		{"prefixed", "overwrite config.yaml with the new value", one, true},
		{"rerun", "rerun the tests after reading", one, true},
		{"punctuated", "read it, then run: go test", one, true},
		{"identifier", "call write_file on out.txt", one, true},
		{"return is not run", "read report.md and return its heading", one, false},
		{"prune is not run", "read the prune policy in docs.md", one, false},
		{"brunch is not run", "read brunch.txt", one, false},
		{"informational", "read report.md and give me its heading", one, false},
		{"decomposed contract trusted", "read a.txt and write b.txt", []Criterion{{ID: "c1"}, {ID: "c2"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestNamesUnaddressedMutation(tt.request, tt.criteria); got != tt.want {
				t.Fatalf("requestNamesUnaddressedMutation(%q) = %v, want %v", tt.request, got, tt.want)
			}
		})
	}
}
