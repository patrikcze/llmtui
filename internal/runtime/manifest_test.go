package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestWriteRead(t *testing.T) {
	tmpDir := t.TempDir()

	m := &Manifest{
		LlamaVersion: "b10066",
		Files: map[string]string{
			"libllama.0.dylib": "abc123",
			"libggml.0.dylib":  "def456",
		},
	}

	// Write
	if err := WriteManifest(tmpDir, m); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	// Verify file exists
	manifestPath := filepath.Join(tmpDir, ManifestFilename)
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("Manifest file not created: %v", err)
	}

	// Read
	m2, err := ReadManifest(tmpDir)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	// Compare
	if m2.LlamaVersion != m.LlamaVersion {
		t.Errorf("LlamaVersion = %q, want %q", m2.LlamaVersion, m.LlamaVersion)
	}
	if len(m2.Files) != len(m.Files) {
		t.Errorf("Files length = %d, want %d", len(m2.Files), len(m.Files))
	}
	for k, v := range m.Files {
		if m2.Files[k] != v {
			t.Errorf("Files[%q] = %q, want %q", k, m2.Files[k], v)
		}
	}
}

func TestReadManifest_NotExists(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ReadManifest(tmpDir)
	if err == nil {
		t.Error("ReadManifest() expected error for non-existent manifest, got nil")
	}
}

func TestReadManifest_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Write invalid JSON
	manifestPath := filepath.Join(tmpDir, ManifestFilename)
	if err := os.WriteFile(manifestPath, []byte("not json"), 0644); err != nil {
		t.Fatalf("Setup: failed to write invalid manifest: %v", err)
	}

	_, err := ReadManifest(tmpDir)
	if err == nil {
		t.Error("ReadManifest() expected error for invalid JSON, got nil")
	}
}

func TestNewManifestFromPin(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	tests := []struct {
		platform string
		wantErr  bool
	}{
		{"darwin-arm64", false},
		{"darwin-amd64", false},
		{"linux-arm64", false},
		{"linux-amd64", false},
		{"windows-amd64", false},
		{"invalid-platform", true},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			m, err := NewManifestFromPin(pin, tt.platform)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewManifestFromPin() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if m.LlamaVersion != pin.LlamaTag {
				t.Errorf("LlamaVersion = %q, want %q", m.LlamaVersion, pin.LlamaTag)
			}
			if len(m.Files) == 0 {
				t.Error("Files map is empty")
			}

			// Verify files match platform pin
			pp := pin.Platforms[tt.platform]
			expected, err := pp.ManagedFiles()
			if err != nil {
				t.Fatalf("ManagedFiles() error = %v", err)
			}
			if len(m.Files) != len(expected) {
				t.Errorf("Files count = %d, want %d", len(m.Files), len(expected))
			}
			for k, v := range expected {
				if m.Files[k] != v {
					t.Errorf("Files[%q] = %q, want %q", k, m.Files[k], v)
				}
			}
		})
	}
}

func TestManifestRoundtrip(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	// Create manifest from pin
	m, err := NewManifestFromPin(pin, CurrentPlatform())
	if err != nil {
		t.Fatalf("NewManifestFromPin() error = %v", err)
	}

	// Write and read back
	tmpDir := t.TempDir()
	if err := WriteManifest(tmpDir, m); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	m2, err := ReadManifest(tmpDir)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}

	// Verify
	if m2.LlamaVersion != m.LlamaVersion {
		t.Errorf("LlamaVersion = %q, want %q", m2.LlamaVersion, m.LlamaVersion)
	}
	if len(m2.Files) != len(m.Files) {
		t.Errorf("Files count = %d, want %d", len(m2.Files), len(m.Files))
	}
}
