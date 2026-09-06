package tui

import "testing"

func TestAskUserEvidenceSummary(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"yes", `{"answer":"yes","grants_authorization":false}`, "user confirmed"},
		{"approved case insensitive", `{"answer":"APPROVED","grants_authorization":false}`, "user confirmed"},
		{"negative", `{"answer":"no","grants_authorization":false}`, "user answer received"},
		{"free text", `{"answer":"report.md","grants_authorization":false}`, "user answer received"},
		{"malformed", `not json`, "user answer received"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := askUserEvidenceSummary(tc.output); got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}
