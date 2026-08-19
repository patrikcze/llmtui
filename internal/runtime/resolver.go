package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Resolution struct {
	Dir      string
	Tier     int
	TierName string
	Verified bool
	Warnings []string
	Attempts []string
}

type ResolveConfig struct {
	ExplicitPath string
	YzmaLib      string
	Platform     *PlatformConfig
	Pin          *Pin
}

func Resolve(cfg ResolveConfig) (*Resolution, error) {
	if cfg.Platform == nil {
		cfg.Platform = DefaultPlatformConfig()
	}
	if cfg.Pin == nil {
		var err error
		cfg.Pin, err = LoadPin()
		if err != nil {
			return nil, fmt.Errorf("load embedded runtime pin: %w", err)
		}
	}
	platformPin, err := cfg.Pin.PlatformPin(cfg.Platform.GOOS + "-" + cfg.Platform.GOARCH)
	if err != nil {
		return nil, err
	}
	if cfg.ExplicitPath != "" {
		return trustedResolution(cfg.ExplicitPath, 1, "explicit config path", cfg.Pin.LlamaTag), nil
	}
	if cfg.YzmaLib != "" {
		return trustedResolution(cfg.YzmaLib, 2, "YZMA_LIB", cfg.Pin.LlamaTag), nil
	}

	attempts := make([]string, 0, 5)
	paths, err := cfg.Platform.ExecutableRelativePaths()
	if err != nil {
		attempts = append(attempts, fmt.Sprintf("bundled runtime: %v", err))
	} else {
		for _, dir := range paths {
			resolution, reason := resolveManaged(dir, 3, "bundled runtime", cfg.Pin.LlamaTag, platformPin, cfg.Platform)
			attempts = append(attempts, fmt.Sprintf("bundled runtime %s: %s", dir, reason))
			if resolution != nil {
				resolution.Attempts = append([]string(nil), attempts...)
				return resolution, nil
			}
		}
	}

	managedDir, err := cfg.Platform.ManagedRuntimeDir(cfg.Pin.LlamaTag)
	if err != nil {
		attempts = append(attempts, fmt.Sprintf("user runtime: %v", err))
	} else {
		resolution, reason := resolveManaged(managedDir, 4, "user runtime", cfg.Pin.LlamaTag, platformPin, cfg.Platform)
		attempts = append(attempts, fmt.Sprintf("user runtime %s: %s", managedDir, reason))
		if resolution != nil {
			resolution.Attempts = append([]string(nil), attempts...)
			return resolution, nil
		}
	}

	legacyDir, err := cfg.Platform.LegacyRuntimeDir()
	if err != nil {
		attempts = append(attempts, fmt.Sprintf("legacy runtime: %v", err))
	} else {
		managedFiles, filesErr := platformPin.ManagedFiles()
		if filesErr != nil {
			return nil, filesErr
		}
		resolution, reason := resolveLegacy(legacyDir, cfg.Pin.LlamaTag, managedFiles, cfg.Platform)
		attempts = append(attempts, fmt.Sprintf("legacy runtime %s: %s", legacyDir, reason))
		if resolution != nil {
			resolution.Attempts = append([]string(nil), attempts...)
			return resolution, nil
		}
	}

	return nil, fmt.Errorf(
		"no valid llama.cpp runtime found; checked:\n  %s\nrun `llmtui runtime install`, or set providers.<name>.library_path or YZMA_LIB",
		strings.Join(attempts, "\n  "),
	)
}

func trustedResolution(path string, tier int, name, expectedVersion string) *Resolution {
	dir := filepath.Clean(ExpandHome(path))
	resolution := &Resolution{Dir: dir, Tier: tier, TierName: name}
	if found, err := ReadVersion(dir); err == nil && !versionMatches(found, expectedVersion) {
		resolution.Warnings = append(resolution.Warnings, fmt.Sprintf("runtime %s differs from tested %s", found, expectedVersion))
	}
	return resolution
}

func resolveManaged(dir string, tier int, name, version string, platformPin *PlatformPin, platform *PlatformConfig) (*Resolution, string) {
	resolved, err := platform.EvalSymlinks(dir)
	if err != nil {
		return nil, err.Error()
	}
	secure, err := platform.IsSecureOwnership(resolved)
	if err != nil {
		return nil, err.Error()
	}
	if !secure {
		return nil, "directory is not owner-controlled"
	}
	foundVersion, err := ReadVersion(resolved)
	if err != nil {
		return nil, err.Error()
	}
	if !versionMatches(foundVersion, version) {
		return nil, fmt.Sprintf("version %q does not match %q", foundVersion, version)
	}
	manifest, err := ReadManifest(resolved)
	if err != nil {
		return nil, err.Error()
	}
	variant, err := platformPin.ForBackend(manifestBackend(manifest))
	if err != nil {
		return nil, err.Error()
	}
	files, err := variant.ManagedFiles()
	if err != nil {
		return nil, err.Error()
	}
	if manifest.LlamaVersion != version || !equalHashes(manifest.Files, files) {
		return nil, "manifest does not match the embedded pin"
	}
	verification, err := VerifyQuickForPlatform(resolved, files, version, platform.GOOS)
	if err != nil {
		return nil, err.Error()
	}
	if !verification.Valid {
		return nil, verificationSummary(verification)
	}
	return &Resolution{Dir: resolved, Tier: tier, TierName: name, Verified: true}, "verified"
}

func resolveLegacy(dir, version string, files map[string]string, platform *PlatformConfig) (*Resolution, string) {
	// Tier 5 reads from the same class of user-owned directory as the
	// managed tiers (3/4) and must be held to the same symlink/ownership
	// standard: a matching LLAMA_VERSION stamp alone is not a trust boundary.
	resolved, err := platform.EvalSymlinks(dir)
	if err != nil {
		return nil, err.Error()
	}
	secure, err := platform.IsSecureOwnership(resolved)
	if err != nil {
		return nil, err.Error()
	}
	if !secure {
		return nil, "directory is not owner-controlled"
	}
	foundVersion, err := ReadVersion(resolved)
	if err != nil {
		return nil, err.Error()
	}
	if !versionMatches(foundVersion, version) {
		return nil, fmt.Sprintf("version %q does not match %q", foundVersion, version)
	}
	verification, err := VerifyBaseline(resolved, files, version, false)
	if err != nil {
		return nil, err.Error()
	}
	if !verification.Valid {
		return nil, verificationSummary(verification)
	}
	return &Resolution{
		Dir:      resolved,
		Tier:     5,
		TierName: "legacy runtime",
		Warnings: []string{"legacy runtime is unverified; run `llmtui runtime install` to migrate"},
	}, "matching legacy stamp"
}

func equalHashes(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, hash := range left {
		if right[name] != hash {
			return false
		}
	}
	return true
}

func versionMatches(stamp, expected string) bool {
	fields := strings.Fields(stamp)
	return len(fields) > 0 && fields[0] == expected
}

func verificationSummary(result *VerificationResult) string {
	parts := make([]string, 0, 2)
	if len(result.MissingFiles) > 0 {
		parts = append(parts, "missing: "+strings.Join(result.MissingFiles, ", "))
	}
	if len(result.BadHashes) > 0 {
		parts = append(parts, "hash mismatch: "+strings.Join(result.BadHashes, ", "))
	}
	return strings.Join(parts, "; ")
}

// ExpandHome expands a leading "~" to the user's home directory. It is a
// no-op (including on lookup failure) for any other path shape.
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
