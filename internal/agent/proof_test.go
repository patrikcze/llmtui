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

func TestMissingFileWriteReceipt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		request   string
		execution ExecutionResult
		want      bool
	}{
		{name: "claimed markdown file without call", request: "Write 30-09-2026-weather.md with the weather report", want: true},
		{name: "claimed file after read", request: "Save the report to a file", execution: ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "read_file", Succeeded: true}}}, want: true},
		{name: "failed write", request: "Create the report file", execution: ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "write_file", Succeeded: false}}}, want: true},
		{name: "confirmed write", request: "Write the report file", execution: ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "write_file", Succeeded: true}}}},
		{name: "confirmed edit", request: "Update the report.md", execution: ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "edit_file", Succeeded: true}}}},
		{name: "informational", request: "Explain how to write a report file", want: false},
		{name: "answer only", request: "Give me the weather report", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MissingFileWriteReceipt(tc.request, tc.execution); got != tc.want {
				t.Fatalf("MissingFileWriteReceipt(%q) = %v, want %v", tc.request, got, tc.want)
			}
		})
	}
}
