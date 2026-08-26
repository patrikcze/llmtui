package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyBaseline_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	testFiles := map[string]string{
		"libllama.so.0": "abc123",
		"libggml.so.0":  "def456",
	}

	for filename := range testFiles {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte("dummy content"), 0644); err != nil {
			t.Fatalf("Setup: failed to create %s: %v", filename, err)
		}
	}

	result, err := VerifyBaseline(tmpDir, testFiles, "b10066", false)
	if err != nil {
		t.Fatalf("VerifyBaseline() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("VerifyBaseline() Valid = false, want true")
	}
}

func TestVerifyBaseline_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"libllama.so.0": "abc123",
		"libggml.so.0":  "def456",
	}

	// Create only one file
	path := filepath.Join(tmpDir, "libllama.so.0")
	if err := os.WriteFile(path, []byte("dummy content"), 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	result, err := VerifyBaseline(tmpDir, testFiles, "b10066", false)
	if err != nil {
		t.Fatalf("VerifyBaseline() error = %v", err)
	}
	if result.Valid {
		t.Errorf("VerifyBaseline() Valid = true, want false")
	}
	if len(result.MissingFiles) != 1 {
		t.Errorf("MissingFiles length = %d, want 1", len(result.MissingFiles))
	}
}

func TestVerifyBaseline_WithManifest(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"libllama.so.0": "abc123",
	}

	// Create test file
	path := filepath.Join(tmpDir, "libllama.so.0")
	if err := os.WriteFile(path, []byte("dummy content"), 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	// Create manifest with matching version
	m := &Manifest{
		LlamaVersion: "b10066",
		Files:        testFiles,
	}
	if err := WriteManifest(tmpDir, m); err != nil {
		t.Fatalf("Setup: failed to write manifest: %v", err)
	}

	result, err := VerifyBaseline(tmpDir, testFiles, "b10066", false)
	if err != nil {
		t.Fatalf("VerifyBaseline() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("VerifyBaseline() Valid = false, want true")
	}
}

func TestVerifyBaseline_ManifestMismatch_StrictMode(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"libllama.so.0": "abc123",
	}

	// Create test file
	path := filepath.Join(tmpDir, "libllama.so.0")
	if err := os.WriteFile(path, []byte("dummy content"), 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	// Create manifest with mismatched version
	m := &Manifest{
		LlamaVersion: "b99999",
		Files:        testFiles,
	}
	if err := WriteManifest(tmpDir, m); err != nil {
		t.Fatalf("Setup: failed to write manifest: %v", err)
	}

	result, err := VerifyBaseline(tmpDir, testFiles, "b10066", false)
	if err != nil {
		t.Fatalf("VerifyBaseline() error = %v", err)
	}
	if result.Valid {
		t.Errorf("VerifyBaseline() Valid = true, want false for version mismatch in strict mode")
	}
}

func TestVerifyBaseline_ManifestMismatch_WarningMode(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"libllama.so.0": "abc123",
	}

	// Create test file
	path := filepath.Join(tmpDir, "libllama.so.0")
	if err := os.WriteFile(path, []byte("dummy content"), 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	// Create manifest with mismatched version
	m := &Manifest{
		LlamaVersion: "b99999",
		Files:        testFiles,
	}
	if err := WriteManifest(tmpDir, m); err != nil {
		t.Fatalf("Setup: failed to write manifest: %v", err)
	}

	result, err := VerifyBaseline(tmpDir, testFiles, "b10066", true)
	if err != nil {
		t.Fatalf("VerifyBaseline() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("VerifyBaseline() Valid = false, want true (warning mode)")
	}
	if len(result.Warnings) == 0 {
		t.Error("Expected warnings for version mismatch, got none")
	}
}

func TestVerifyFull_Success(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files with known content
	testContent := []byte("test content for hashing")
	testFiles := map[string]string{
		"test1.txt": computeTestHash(t, testContent),
		"test2.txt": computeTestHash(t, testContent),
	}

	for filename := range testFiles {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, testContent, 0644); err != nil {
			t.Fatalf("Setup: failed to create %s: %v", filename, err)
		}
	}

	result, err := VerifyFull(tmpDir, testFiles)
	if err != nil {
		t.Fatalf("VerifyFull() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("VerifyFull() Valid = false, want true; bad hashes: %v", result.BadHashes)
	}
}

func TestVerifyFull_HashMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"test.txt": "wronghash0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}

	path := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(path, []byte("actual content"), 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	result, err := VerifyFull(tmpDir, testFiles)
	if err != nil {
		t.Fatalf("VerifyFull() error = %v", err)
	}
	if result.Valid {
		t.Errorf("VerifyFull() Valid = true, want false")
	}
	if len(result.BadHashes) != 1 {
		t.Errorf("BadHashes length = %d, want 1", len(result.BadHashes))
	}
}

func TestVerifyQuick(t *testing.T) {
	tmpDir := t.TempDir()

	// Create primary library with known content
	primaryLib := getPrimaryLibraryName()
	testContent := []byte("primary library content")
	testFiles := map[string]string{
		primaryLib: computeTestHash(t, testContent),
	}

	path := filepath.Join(tmpDir, primaryLib)
	if err := os.WriteFile(path, testContent, 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	result, err := VerifyQuick(tmpDir, testFiles, "b10066")
	if err != nil {
		t.Fatalf("VerifyQuick() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("VerifyQuick() Valid = false, want true; missing: %v, bad: %v", result.MissingFiles, result.BadHashes)
	}
}

func TestComputeFileSHA256(t *testing.T) {
	tmpDir := t.TempDir()

	content := []byte("test content")
	path := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("Setup: failed to create file: %v", err)
	}

	hash, err := computeFileSHA256(path)
	if err != nil {
		t.Fatalf("computeFileSHA256() error = %v", err)
	}

	expected := computeTestHash(t, content)
	if hash != expected {
		t.Errorf("computeFileSHA256() = %q, want %q", hash, expected)
	}
}

func TestGetPrimaryLibraryName(t *testing.T) {
	name := getPrimaryLibraryName()
	if name == "" {
		t.Error("getPrimaryLibraryName() returned empty string")
	}

	// Should be platform-appropriate
	platform := CurrentPlatform()
	switch platform {
	case "darwin-arm64", "darwin-amd64":
		if name != "libllama.dylib" {
			t.Errorf("getPrimaryLibraryName() = %q, want libllama.dylib for macOS", name)
		}
	case "linux-arm64", "linux-amd64":
		if name != "libllama.so" {
			t.Errorf("getPrimaryLibraryName() = %q, want libllama.so for Linux", name)
		}
	case "windows-amd64":
		if name != "llama.dll" {
			t.Errorf("getPrimaryLibraryName() = %q, want llama.dll for Windows", name)
		}
	}
}

// computeTestHash is a helper that computes SHA256 for test content
func computeTestHash(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "temp")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write hash fixture: %v", err)
	}
	hash, err := computeFileSHA256(path)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	return hash
}
