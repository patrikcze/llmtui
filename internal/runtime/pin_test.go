package runtime

import (
	"testing"
)

func TestLoadPin(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	// Verify structure
	if pin.YzmaVersion != "v1.19.0" {
		t.Errorf("YzmaVersion = %q, want v1.19.0", pin.YzmaVersion)
	}
	if pin.LlamaTag != "b10066" {
		t.Errorf("LlamaTag = %q, want b10066", pin.LlamaTag)
	}
	if pin.LlamaCommit != "86a9c79f866799eb0e7e89c03578ccfbcc5d808e" {
		t.Errorf("LlamaCommit = %q, want 86a9c79f866799eb0e7e89c03578ccfbcc5d808e", pin.LlamaCommit)
	}

	// Verify compatible range
	if pin.CompatibleRange.Min != "b9979" {
		t.Errorf("CompatibleRange.Min = %q, want b9979", pin.CompatibleRange.Min)
	}
	if pin.CompatibleRange.Max != "b10103" {
		t.Errorf("CompatibleRange.Max = %q, want b10103", pin.CompatibleRange.Max)
	}

	// Verify all platforms exist
	expectedPlatforms := []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64", "windows-amd64"}
	for _, platform := range expectedPlatforms {
		pp, ok := pin.Platforms[platform]
		if !ok {
			t.Errorf("Platform %q not found in pin", platform)
			continue
		}

		// Verify essential fields
		if pp.Archive == "" {
			t.Errorf("Platform %q: Archive is empty", platform)
		}
		if pp.URL == "" {
			t.Errorf("Platform %q: URL is empty", platform)
		}
		if pp.SHA256 == "" {
			t.Errorf("Platform %q: SHA256 is empty", platform)
		}
		if pp.Size == 0 {
			t.Errorf("Platform %q: Size is zero", platform)
		}
		if len(pp.Files) == 0 {
			t.Errorf("Platform %q: Files map is empty", platform)
		}
		managedFiles, err := pp.ManagedFiles()
		if err != nil {
			t.Errorf("Platform %q: ManagedFiles() error = %v", platform, err)
			continue
		}

		// Verify LICENSE exists in files
		if _, ok := pp.Files["LICENSE"]; !ok {
			t.Errorf("Platform %q: LICENSE not in Files", platform)
		}

		// Verify platform-specific primary library
		switch platform {
		case "darwin-arm64", "darwin-amd64":
			if _, ok := managedFiles["libllama.dylib"]; !ok {
				t.Errorf("Platform %q: libllama.dylib not in managed files", platform)
			}
			if _, ok := managedFiles["libggml.dylib"]; !ok {
				t.Errorf("Platform %q: libggml.dylib not in managed files", platform)
			}
		case "linux-arm64", "linux-amd64":
			if _, ok := managedFiles["libllama.so"]; !ok {
				t.Errorf("Platform %q: libllama.so not in managed files", platform)
			}
			if _, ok := managedFiles["libggml.so"]; !ok {
				t.Errorf("Platform %q: libggml.so not in managed files", platform)
			}
		case "windows-amd64":
			if _, ok := pp.Files["llama.dll"]; !ok {
				t.Errorf("Platform %q: llama.dll not in Files", platform)
			}
			if _, ok := pp.Files["ggml.dll"]; !ok {
				t.Errorf("Platform %q: ggml.dll not in Files", platform)
			}
		}
	}
}

func TestGetPlatformPin(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	// Test current platform
	pp, err := pin.GetPlatformPin()
	if err != nil {
		t.Fatalf("GetPlatformPin() error = %v", err)
	}
	if pp == nil {
		t.Fatal("GetPlatformPin() returned nil")
	}
	if len(pp.Files) == 0 {
		t.Error("GetPlatformPin() returned empty Files map")
	}
}

func TestCurrentPlatform(t *testing.T) {
	platform := CurrentPlatform()
	if platform == "" {
		t.Error("CurrentPlatform() returned empty string")
	}

	// Should be in the format "os-arch"
	expectedPlatforms := map[string]bool{
		"darwin-arm64":  true,
		"darwin-amd64":  true,
		"linux-arm64":   true,
		"linux-amd64":   true,
		"windows-amd64": true,
		"windows-arm64": true,
		"linux-386":     true,
		"darwin-386":    true,
	}

	if !expectedPlatforms[platform] {
		t.Logf("CurrentPlatform() = %q (uncommon but valid)", platform)
	}
}

func TestLlamaVersion(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	version := pin.LlamaVersion()
	if version != "b10066" {
		t.Errorf("LlamaVersion() = %q, want b10066", version)
	}
}

func TestPlatformPinHashesFormat(t *testing.T) {
	pin, err := LoadPin()
	if err != nil {
		t.Fatalf("LoadPin() error = %v", err)
	}

	for platform, pp := range pin.Platforms {
		// Archive SHA256 should be 64 hex chars
		if len(pp.SHA256) != 64 {
			t.Errorf("Platform %q: archive SHA256 length = %d, want 64", platform, len(pp.SHA256))
		}

		// All file hashes should be 64 hex chars
		for filename, hash := range pp.Files {
			if len(hash) != 64 {
				t.Errorf("Platform %q, file %q: SHA256 length = %d, want 64", platform, filename, len(hash))
			}
			// Quick hex validation (all chars should be 0-9a-f)
			for _, c := range hash {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("Platform %q, file %q: SHA256 contains non-hex char %q", platform, filename, c)
					break
				}
			}
		}
		for backend, pack := range pp.Packs {
			if len(pack.SHA256) != 64 || pack.Archive == "" || pack.URL == "" || pack.Size <= 0 {
				t.Errorf("Platform %q backend %q has incomplete archive pin", platform, backend)
			}
			for filename, hash := range pack.Files {
				if len(hash) != 64 {
					t.Errorf("Platform %q backend %q file %q: SHA256 length = %d, want 64", platform, backend, filename, len(hash))
				}
			}
		}
	}
}
