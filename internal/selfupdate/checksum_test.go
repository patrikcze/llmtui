package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	h := strings.Repeat("a", 64)
	g := strings.Repeat("b", 64)
	in := "" +
		"# a comment\n" +
		h + "  ./llmtui-v1.0.24-linux-amd64.tar.gz\n" +
		g + " *llmtui-v1.0.24-windows-amd64.zip\n" +
		"\n"
	sums, err := ParseChecksums(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if got, _ := sums.For("llmtui-v1.0.24-linux-amd64.tar.gz"); got != h {
		t.Errorf("linux hash = %q", got)
	}
	if got, _ := sums.For("./llmtui-v1.0.24-windows-amd64.zip"); got != g {
		t.Errorf("windows hash = %q", got)
	}
	if _, ok := sums.For("missing"); ok {
		t.Error("unexpected hit for missing entry")
	}
}

func TestParseChecksumsErrors(t *testing.T) {
	cases := map[string]string{
		"empty":        "\n\n",
		"short hash":   "abc  file\n",
		"no name":      strings.Repeat("a", 64) + "\n",
		"non-hex hash": strings.Repeat("z", 64) + "  file\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChecksums(strings.NewReader(in)); err == nil {
				t.Fatalf("expected error for %q", in)
			}
		})
	}
}

func TestVerifyFileChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob")
	data := []byte("hello llmtui")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	if err := VerifyFileChecksum(p, want); err != nil {
		t.Fatalf("matching checksum rejected: %v", err)
	}
	if err := VerifyFileChecksum(p, strings.ToUpper(want)); err != nil {
		t.Fatalf("case-insensitive match rejected: %v", err)
	}
	if err := VerifyFileChecksum(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("mismatched checksum accepted")
	}
}
