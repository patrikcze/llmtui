package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/patrikcze/llmtui/internal/atomicfile"
)

// InstallManifest is a small, non-sensitive record written by `self install`
// and `self update` so `self path` and later updates can identify a managed
// installation confidently. It contains no secrets and no absolute paths that
// are trusted without re-validation.
type InstallManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	Scope         string `json:"scope"`
	Prefix        string `json:"prefix"`
	Asset         string `json:"asset,omitempty"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	InstalledAt   string `json:"installed_at"`
	InstalledBy   string `json:"installed_by"` // "self install" | "self update"
}

const manifestSchemaVersion = 1

// WriteManifest writes m to path atomically with 0o644 permissions: the
// manifest holds nothing secret and other users of a system-wide install
// must be able to read it.
func WriteManifest(path string, m InstallManifest) error {
	m.SchemaVersion = manifestSchemaVersion
	if m.InstalledAt == "" {
		m.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("install manifest: %w", err)
	}
	return nil
}

// ReadManifest loads an install manifest. A missing file returns
// (nil, os.ErrNotExist wrapped).
func ReadManifest(path string) (*InstallManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m InstallManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse install manifest %s: %w", path, err)
	}
	return &m, nil
}
