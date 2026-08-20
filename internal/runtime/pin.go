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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"sync"
)

//go:embed pin.json
var embeddedPinJSON []byte

// embeddedLlamaCppLicense is llama.cpp's own MIT LICENSE text at the pinned
// tag, byte-identical to the LICENSE file bundled in the macOS/Linux release
// archives (verified: sha256 94f29bbe...d010d, matching every platform entry
// in pin.json). The official Windows CPU/Vulkan zip does not include a
// LICENSE file at all — Install falls back to writing this verified copy for
// any platform whose archive doesn't provide one, so every managed runtime
// directory ends up with the upstream license regardless of how a given
// platform's release happens to be packaged upstream.
//
//go:embed llama-cpp-LICENSE.txt
var embeddedLlamaCppLicense []byte

var fallbackLicenseHash = sync.OnceValue(func() string {
	sum := sha256.Sum256(embeddedLlamaCppLicense)
	return hex.EncodeToString(sum[:])
})

// fallbackLicenseSHA256 returns the SHA256 of llmtui's embedded llama.cpp
// LICENSE fallback, computed once from the actual embedded bytes so it can
// never drift from embeddedLlamaCppLicense's real content.
func fallbackLicenseSHA256() string {
	return fallbackLicenseHash()
}

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
// Every managed directory is guaranteed a "LICENSE" entry: platforms whose
// archive includes one (verified via that entry's own hash in Files) keep
// it; platforms whose official archive doesn't (the Windows CPU/Vulkan zips,
// as of b10066) get llmtui's embedded, byte-identical fallback copy of the
// same upstream text (see Install's ensureLicensePresent). Encoding the
// guarantee here — rather than only inside Install — is what keeps
// install-time manifest writing and resolve-time pin comparison in
// agreement regardless of which path supplied the file.
func (p PlatformPin) ManagedFiles() (map[string]string, error) {
	files := make(map[string]string, len(p.Files)+len(p.Aliases)+1)
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
	if _, ok := files["LICENSE"]; !ok {
		files["LICENSE"] = fallbackLicenseSHA256()
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
