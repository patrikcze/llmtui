package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extractInto(t *testing.T, archiveName string, data []byte, goos string) (*Payload, error) {
	t.Helper()
	work := t.TempDir()
	ap := writeTemp(t, work, archiveName, data)
	dest := t.TempDir()
	return ExtractRelease(ap, dest, goos)
}

func TestExtractReleaseTarGz(t *testing.T) {
	data := buildTarGz(t, defaultReleaseEntries("llmtui-v1.0.24-linux-amd64", "linux"))
	p, err := extractInto(t, "llmtui-v1.0.24-linux-amd64.tar.gz", data, "linux")
	if err != nil {
		t.Fatalf("ExtractRelease: %v", err)
	}
	defer func() { _ = os.RemoveAll(p.Dir) }()

	if _, err := os.Stat(p.BinPath); err != nil {
		t.Fatalf("binary missing: %v", err)
	}
	if p.RuntimeDir == "" {
		t.Fatal("runtime dir not detected")
	}
	if _, err := os.Stat(filepath.Join(p.RuntimeDir, "libllama.so")); err != nil {
		t.Fatalf("runtime file missing: %v", err)
	}
	if len(p.DocFiles) != 2 {
		t.Errorf("DocFiles = %v", p.DocFiles)
	}
	// examples/ must not be extracted.
	if _, err := os.Stat(filepath.Join(p.Dir, "examples")); !os.IsNotExist(err) {
		t.Errorf("examples/ was extracted (err=%v)", err)
	}
}

func TestExtractReleaseZip(t *testing.T) {
	data := buildZip(t, defaultReleaseEntries("llmtui-v1.0.24-windows-amd64", "windows"))
	p, err := extractInto(t, "llmtui-v1.0.24-windows-amd64.zip", data, "windows")
	if err != nil {
		t.Fatalf("ExtractRelease: %v", err)
	}
	defer func() { _ = os.RemoveAll(p.Dir) }()
	if filepath.Base(p.BinPath) != "llmtui.exe" {
		t.Errorf("BinPath = %s", p.BinPath)
	}
	if p.RuntimeDir == "" {
		t.Error("runtime dir not detected in zip")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	entries := []archiveEntry{
		{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
		{name: "llmtui-v1.0.24-linux-amd64/../evil", body: []byte("x"), mode: 0o644},
	}
	if _, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux"); err == nil {
		t.Fatal("traversal path accepted")
	}
}

func TestExtractRejectsAbsolutePath(t *testing.T) {
	entries := []archiveEntry{
		{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
		{name: "/etc/evil", body: []byte("x"), mode: 0o644},
	}
	if _, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux"); err == nil {
		t.Fatal("absolute path accepted")
	}
}

func TestExtractRejectsUnsafeSymlink(t *testing.T) {
	for _, target := range []string{"/etc/passwd", "../../escape", "sub/dir/file"} {
		entries := []archiveEntry{
			{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
			{name: "llmtui-v1.0.24-linux-amd64/llmtui", body: fakeBinary("linux"), mode: 0o755},
			{name: "llmtui-v1.0.24-linux-amd64/lib/llmtui/runtime/evil.so", symlink: target},
		}
		_, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux")
		if err == nil || !strings.Contains(err.Error(), "unsafe symlink") {
			t.Fatalf("unsafe symlink %q accepted: %v", target, err)
		}
	}
}

func TestExtractAllowsRuntimeSonameSymlink(t *testing.T) {
	entries := append(defaultReleaseEntries("llmtui-v1.0.24-linux-amd64", "linux"),
		archiveEntry{name: "llmtui-v1.0.24-linux-amd64/lib/llmtui/runtime/libllama.so.0", symlink: "libllama.so"})
	p, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux")
	if err != nil {
		t.Fatalf("safe soname symlink rejected: %v", err)
	}
	defer func() { _ = os.RemoveAll(p.Dir) }()
	link := filepath.Join(p.RuntimeDir, "libllama.so.0")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("soname symlink not created: %v", err)
	}
	if tgt, _ := os.Readlink(link); tgt != "libllama.so" {
		t.Errorf("symlink target = %q, want libllama.so", tgt)
	}
}

func TestExtractRejectsSecondTopDir(t *testing.T) {
	entries := []archiveEntry{
		{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
		{name: "llmtui-v1.0.24-linux-amd64/llmtui", body: fakeBinary("linux"), mode: 0o755},
		{name: "somewhere-else/llmtui", body: fakeBinary("linux"), mode: 0o755},
	}
	if _, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux"); err == nil {
		t.Fatal("entry outside the release directory accepted")
	}
}

func TestExtractRequiresBinary(t *testing.T) {
	entries := []archiveEntry{
		{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
		{name: "llmtui-v1.0.24-linux-amd64/LICENSE", body: []byte("x"), mode: 0o644},
	}
	if _, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux"); err == nil {
		t.Fatal("archive without a binary accepted")
	}
}

func TestExtractRejectsOversizedEntry(t *testing.T) {
	orig := maxExtractFileBytes
	maxExtractFileBytes = 1024
	t.Cleanup(func() { maxExtractFileBytes = orig })

	entries := []archiveEntry{
		{name: "llmtui-v1.0.24-linux-amd64/", dir: true},
		{name: "llmtui-v1.0.24-linux-amd64/llmtui", body: fakeBinary("linux"), mode: 0o755},
	}
	if _, err := extractInto(t, "a.tar.gz", buildTarGz(t, entries), "linux"); err == nil {
		t.Fatal("entry over the per-file cap accepted")
	}
}

func TestExtractRejectsArchiveBomb(t *testing.T) {
	orig := maxExtractTotalBytes
	maxExtractTotalBytes = 1500
	t.Cleanup(func() { maxExtractTotalBytes = orig })

	// fakeBinary is ~700 KiB; the total cap of 1500 bytes is blown by the
	// binary alone.
	if _, err := extractInto(t, "a.tar.gz",
		buildTarGz(t, defaultReleaseEntries("llmtui-v1.0.24-linux-amd64", "linux")), "linux"); err == nil {
		t.Fatal("payload over the total cap accepted")
	}
}

func TestValidateBinary(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, fakeBinary(hostGOOS), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBinary(good, hostGOOS); err != nil {
		t.Fatalf("valid binary rejected: %v", err)
	}

	small := filepath.Join(dir, "small")
	if err := os.WriteFile(small, []byte("\x7fELF tiny"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBinary(small, "linux"); err == nil {
		t.Fatal("tiny binary accepted")
	}

	wrongMagic := filepath.Join(dir, "wrong")
	b := make([]byte, 700<<10)
	copy(b, []byte("junk"))
	if err := os.WriteFile(wrongMagic, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBinary(wrongMagic, "linux"); err == nil {
		t.Fatal("wrong-magic binary accepted for linux")
	}
}
