package redact

import "testing"

func TestSecrets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "key value assignment",
			input: "api_key=sk-live-abcdef1234567890",
			want:  "api_key=[REDACTED]",
		},
		{
			name:  "bearer token",
			input: "Authorization header: Bearer abcDEF012345.67~89",
			want:  "Authorization header: Bearer [REDACTED]",
		},
		{
			name:  "sk-style api key without assignment",
			input: "found key ghp-abcdefgh12345678 in output",
			want:  "found key [REDACTED KEY] in output",
		},
		{
			name:  "private key block",
			input: "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIB...\n-----END RSA PRIVATE KEY-----\nafter",
			want:  "before\n[REDACTED PRIVATE KEY]\nafter",
		},
		{
			name:  "no secret shape",
			input: "plain observable text with no credentials",
			want:  "plain observable text with no credentials",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Secrets(tc.input); got != tc.want {
				t.Errorf("Secrets(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
