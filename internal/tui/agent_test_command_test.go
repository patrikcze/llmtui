package tui

import "testing"

func TestLooksLikeTestCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"go test ./...", true},
		{"  GO TEST -run X ./pkg", true},
		{"pytest", true},
		{"cd internal/agent && go test ./...", true},
		{"(cd web && npm test)", true},
		{"go build ./... ; go vet ./...", true},
		{"CGO_ENABLED=0 go test -count=1 ./...", true},
		{"make build || make test", true},
		{"go test ./... | tee out.log", true},
		{"go testdata", false},
		{"echo go test", false},
		{"cd go-test && ls", false},
		{"go build ./...", false},
		{"cat pytest.ini", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := looksLikeTestCommand(tt.command); got != tt.want {
				t.Fatalf("looksLikeTestCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}
