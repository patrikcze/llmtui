package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTrustedPrecedence(t *testing.T) {
	resolution, err := Resolve(ResolveConfig{ExplicitPath: "config", YzmaLib: "env", Pin: testPin(), Platform: testPlatform(t)})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Tier != 1 || resolution.Dir != "config" || resolution.Verified {
		t.Fatalf("resolution = %+v", resolution)
	}
	resolution, err = Resolve(ResolveConfig{YzmaLib: "env", Pin: testPin(), Platform: testPlatform(t)})
	if err != nil || resolution.Tier != 2 {
		t.Fatalf("resolution = %+v, error = %v", resolution, err)
	}
}

func TestResolveManagedTiers(t *testing.T) {
	for _, tier := range []int{3, 4} {
		t.Run(string(rune('0'+tier)), func(t *testing.T) {
			root := t.TempDir()
			pin := testPin()
			platform := testPlatform(t)
			var dir string
			if tier == 3 {
				dir = filepath.Join(root, "lib", "llmtui", "runtime")
				platform.ExecutablePath = func() (string, error) { return filepath.Join(root, "llmtui"), nil }
			} else {
				dir = filepath.Join(root, "llmtui", "runtime", pin.LlamaTag)
				platform.DataDir = func() (string, error) { return root, nil }
			}
			writeManaged(t, dir, pin)
			resolution, err := Resolve(ResolveConfig{Pin: pin, Platform: platform})
			if err != nil {
				t.Fatal(err)
			}
			if resolution.Tier != tier || !resolution.Verified {
				t.Fatalf("resolution = %+v", resolution)
			}
		})
	}
}

func TestResolveManagedRejectsTampering(t *testing.T) {
	root := t.TempDir()
	pin := testPin()
	dir := filepath.Join(root, "llmtui", "runtime", pin.LlamaTag)
	writeManaged(t, dir, pin)
	if err := os.WriteFile(filepath.Join(dir, "libllama.so"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	platform := testPlatform(t)
	platform.DataDir = func() (string, error) { return root, nil }
	_, err := Resolve(ResolveConfig{Pin: pin, Platform: platform})
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveManagedRejectsInsecureDirectory(t *testing.T) {
	root := t.TempDir()
	pin := testPin()
	dir := filepath.Join(root, "llmtui", "runtime", pin.LlamaTag)
	writeManaged(t, dir, pin)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	platform := testPlatform(t)
	platform.DataDir = func() (string, error) { return root, nil }
	_, err := Resolve(ResolveConfig{Pin: pin, Platform: platform})
	if err == nil || !strings.Contains(err.Error(), "owner-controlled") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveLegacyUsesVersionStamp(t *testing.T) {
	root := t.TempDir()
	pin := testPin()
	dir := filepath.Join(root, ".local", "share", "llmtui", "llama.cpp")
	managedFiles, err := pin.Platforms["linux-amd64"].ManagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, managedFiles)
	if err := WriteVersion(dir, pin.LlamaTag); err != nil {
		t.Fatal(err)
	}
	platform := testPlatform(t)
	platform.HomeDir = func() (string, error) { return root, nil }
	resolution, err := Resolve(ResolveConfig{Pin: pin, Platform: platform})
	if err != nil || resolution.Tier != 5 || len(resolution.Warnings) == 0 {
		t.Fatalf("resolution = %+v, error = %v", resolution, err)
	}
}

func TestResolveErrorIsActionable(t *testing.T) {
	_, err := Resolve(ResolveConfig{Pin: testPin(), Platform: testPlatform(t)})
	if err == nil || !strings.Contains(err.Error(), "llmtui runtime install") || !strings.Contains(err.Error(), "bundled runtime") {
		t.Fatalf("error = %v", err)
	}
}

func testPin() *Pin {
	hash := sha256.Sum256([]byte("runtime"))
	return &Pin{LlamaTag: "b10066", Platforms: map[string]PlatformPin{
		"linux-amd64": {Files: map[string]string{"libllama.so": hex.EncodeToString(hash[:])}},
	}}
}

func testPlatform(t *testing.T) *PlatformConfig {
	t.Helper()
	root := t.TempDir()
	return &PlatformConfig{
		ExecutablePath: func() (string, error) { return filepath.Join(root, "bin", "llmtui"), nil },
		HomeDir:        func() (string, error) { return filepath.Join(root, "home"), nil },
		DataDir:        func() (string, error) { return filepath.Join(root, "data"), nil },
		GOOS:           "linux",
		GOARCH:         "amd64",
		Stat:           os.Stat,
		EvalSymlinks:   func(path string) (string, error) { return path, nil },
	}
}

func writeManaged(t *testing.T, dir string, pin *Pin) {
	t.Helper()
	files, err := pin.Platforms["linux-amd64"].ManagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, files)
	if err := WriteManifest(dir, &Manifest{LlamaVersion: pin.LlamaTag, Files: files}); err != nil {
		t.Fatal(err)
	}
}

// writeFiles writes fixture files matching the hashes ManagedFiles()
// expects. Every fake test fixture file hashes to sha256("runtime") except
// "LICENSE", which ManagedFiles() always guarantees via the real embedded
// fallback text (see PlatformPin.ManagedFiles), so it needs real content.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		content := []byte("runtime")
		if name == "LICENSE" {
			content = embeddedLlamaCppLicense
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
