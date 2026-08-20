package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestFilename is the name of the manifest file stored in managed
// runtime directories.
const ManifestFilename = "LLMTUI_MANIFEST.json"

// VersionFilename is the llama.cpp release stamp shared with legacy installs.
const VersionFilename = "LLAMA_VERSION"

// Manifest records the llama.cpp version and per-file SHA256 hashes for
// a managed runtime directory. The installer (Phase 2) writes this after
// extracting/validating files; resolver tiers 3-4 require it.
type Manifest struct {
	LlamaVersion string            `json:"llama_version"` // e.g. "b10066"
	Backend      string            `json:"backend"`
	Files        map[string]string `json:"files"` // filename → SHA256
}

// WriteManifest writes a manifest to dir/LLMTUI_MANIFEST.json.
func WriteManifest(dir string, m *Manifest) error {
	path := filepath.Join(dir, ManifestFilename)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return WriteVersion(dir, m.LlamaVersion)
}

// ReadManifest reads the manifest from dir/LLMTUI_MANIFEST.json.
// Returns an error if the file does not exist or cannot be parsed.
func ReadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return &m, nil
}

func manifestBackend(manifest *Manifest) string {
	if manifest.Backend == "" {
		return "cpu"
	}
	return manifest.Backend
}

// WriteVersion writes the pinned llama.cpp tag used by managed and legacy runtimes.
func WriteVersion(dir, version string) error {
	path := filepath.Join(dir, VersionFilename)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(version)+"\n"), 0644); err != nil {
		return fmt.Errorf("write version stamp %s: %w", path, err)
	}
	return nil
}

// ReadVersion reads and trims a runtime's LLAMA_VERSION stamp.
func ReadVersion(dir string) (string, error) {
	path := filepath.Join(dir, VersionFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read version stamp %s: %w", path, err)
	}
	version := strings.TrimSpace(string(data))
	if version == "" {
		return "", fmt.Errorf("version stamp %s is empty", path)
	}
	return version, nil
}

// NewManifestFromPin creates a Manifest from a Pin's platform files,
// suitable for writing to a managed directory after installation.
func NewManifestFromPin(pin *Pin, platform string) (*Manifest, error) {
	return NewManifestForBackend(pin, platform, "cpu")
}

// NewManifestForBackend creates a managed manifest for one runtime variant.
func NewManifestForBackend(pin *Pin, platform, backend string) (*Manifest, error) {
	pp, ok := pin.Platforms[platform]
	if !ok {
		return nil, fmt.Errorf("platform %q not found in pin", platform)
	}
	variant, err := pp.ForBackend(backend)
	if err != nil {
		return nil, err
	}
	files, err := variant.ManagedFiles()
	if err != nil {
		return nil, err
	}
	return &Manifest{
		LlamaVersion: pin.LlamaTag,
		Backend:      backend,
		Files:        files,
	}, nil
}
