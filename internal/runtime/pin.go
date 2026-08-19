// Package runtime provides embedded llama.cpp runtime resolution, verification,
// and manifest management. It resolves runtime libraries across multiple tiers
// (explicit config, environment, executable-relative, user data, legacy),
// verifies integrity against embedded trusted hashes, and supports dependency
// injection for testability.
//
// All filesystem operations are read-only at resolution time. The installer
// (Phase 2) will use the same Pin and Manifest types for writing managed tiers.
package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	goruntime "runtime"
)

//go:embed pin.json
var embeddedPinJSON []byte

// Pin describes the canonical llama.cpp runtime pin: yzma version, llama.cpp
// tag/commit, compatible range, and per-platform archive URLs, SHA256 hashes,
// and per-file SHA256 hashes for all runtime files required by managed tiers.
type Pin struct {
	YzmaVersion     string                 `json:"yzma_version"`
	LlamaTag        string                 `json:"llama_tag"`
	LlamaCommit     string                 `json:"llama_commit"`
	CompatibleRange CompatibleRange        `json:"compatible_range"`
	Platforms       map[string]PlatformPin `json:"platforms"`
}

// CompatibleRange specifies the llama.cpp build range compatible with the
// pinned yzma version (e.g., b9979..b10103 for yzma v1.19.0).
type CompatibleRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

// PlatformPin describes one platform's official llama.cpp release archive
// and the per-file SHA256 hashes for all runtime files we bundle/verify.
type PlatformPin struct {
	Archive string             `json:"archive"` // e.g. "llama-b10066-bin-macos-arm64.tar.gz"
	URL     string             `json:"url"`
	SHA256  string             `json:"sha256"`  // archive SHA256
	Size    int64              `json:"size"`    // archive size in bytes
	Files   map[string]string  `json:"files"`   // filename → SHA256
	Aliases map[string]string  `json:"aliases"` // filename → trusted target filename
	Packs   map[string]PackPin `json:"packs"`   // optional complete archive variants
}

// PackPin replaces the base archive and adds backend-specific regular files.
// All unlisted payload files and aliases must match the base platform pin.
type PackPin struct {
	Archive string            `json:"archive"`
	URL     string            `json:"url"`
	SHA256  string            `json:"sha256"`
	Size    int64             `json:"size"`
	Files   map[string]string `json:"files"`
}

// ForBackend returns the complete archive/file pin for a backend variant.
func (p PlatformPin) ForBackend(backend string) (*PlatformPin, error) {
	if backend == "" || backend == "cpu" {
		return &p, nil
	}
	pack, ok := p.Packs[backend]
	if !ok {
		return nil, fmt.Errorf("backend %q is not available for this platform", backend)
	}
	variant := p
	variant.Archive = pack.Archive
	variant.URL = pack.URL
	variant.SHA256 = pack.SHA256
	variant.Size = pack.Size
	variant.Files = make(map[string]string, len(p.Files)+len(pack.Files))
	for name, hash := range p.Files {
		variant.Files[name] = hash
	}
	for name, hash := range pack.Files {
		variant.Files[name] = hash
	}
	variant.Packs = nil
	return &variant, nil
}

// ManagedFiles returns hashes for both archive payloads and trusted aliases.
func (p PlatformPin) ManagedFiles() (map[string]string, error) {
	files := make(map[string]string, len(p.Files)+len(p.Aliases))
	for name, hash := range p.Files {
		files[name] = hash
	}
	for name, target := range p.Aliases {
		hash, ok := p.Files[target]
		if !ok {
			return nil, fmt.Errorf("runtime alias %q references unknown target %q", name, target)
		}
		files[name] = hash
	}
	return files, nil
}

// LoadPin parses the embedded pin.json.
var LoadPin = func() (*Pin, error) {
	var pin Pin
	if err := json.Unmarshal(embeddedPinJSON, &pin); err != nil {
		return nil, fmt.Errorf("parse embedded pin.json: %w", err)
	}
	return &pin, nil
}

// CurrentPlatform returns the platform key for the current GOOS/GOARCH
// (e.g., "darwin-arm64", "linux-amd64", "windows-amd64").
var CurrentPlatform = func() string {
	return goruntime.GOOS + "-" + goruntime.GOARCH
}

// GetPlatformPin returns the PlatformPin for the current platform, or an error
// if the platform is not supported in the embedded pin.
func (p *Pin) GetPlatformPin() (*PlatformPin, error) {
	return p.PlatformPin(CurrentPlatform())
}

// PlatformPin returns the pin for a specific GOOS-GOARCH key.
func (p *Pin) PlatformPin(platform string) (*PlatformPin, error) {
	pp, ok := p.Platforms[platform]
	if !ok {
		return nil, fmt.Errorf("platform %q not supported in embedded pin (available: darwin-arm64, darwin-amd64, linux-arm64, linux-amd64, windows-amd64)", platform)
	}
	return &pp, nil
}

// LlamaVersion returns the LLAMA_VERSION string expected by the manifest
// (e.g., "b10066" for llama.cpp tag b10066).
func (p *Pin) LlamaVersion() string {
	return p.LlamaTag
}
