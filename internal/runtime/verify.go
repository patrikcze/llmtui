package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
)

// VerificationResult records the outcome of verifying a runtime directory.
type VerificationResult struct {
	Valid        bool     // true if all required files exist and match expected hashes
	MissingFiles []string // files expected but not found
	BadHashes    []string // files with mismatched SHA256
	Warnings     []string // non-fatal issues (e.g., stamp mismatch for tier 1-2)
}

// VerifyBaseline checks that the expected runtime files exist in dir and
// optionally validates their sizes. This is the fast check used by
// HealthCheck and tier 1-2 resolution. It does NOT hash files.
//
// expectedFiles is a map of filename → expected SHA256 (from Pin or Manifest).
// If stampMismatchIsWarning is true, a LLAMA_VERSION mismatch produces a
// warning instead of an error (tier 1-2 behavior).
func VerifyBaseline(dir string, expectedFiles map[string]string, expectedVersion string, stampMismatchIsWarning bool) (*VerificationResult, error) {
	result := &VerificationResult{Valid: true}

	// Check for manifest if this looks like a managed dir
	manifestPath := filepath.Join(dir, ManifestFilename)
	if _, err := os.Stat(manifestPath); err == nil {
		// Manifest exists, read and check version
		m, err := ReadManifest(dir)
		if err != nil {
			if stampMismatchIsWarning {
				result.Warnings = append(result.Warnings, fmt.Sprintf("manifest read error: %v", err))
			} else {
				result.Valid = false
				result.MissingFiles = append(result.MissingFiles, ManifestFilename)
			}
		} else if m.LlamaVersion != expectedVersion {
			msg := fmt.Sprintf("LLAMA_VERSION mismatch: found %q, expected %q", m.LlamaVersion, expectedVersion)
			if stampMismatchIsWarning {
				result.Warnings = append(result.Warnings, msg)
			} else {
				result.Valid = false
				result.Warnings = append(result.Warnings, msg)
			}
		}
	}

	checkFilesExist(result, dir, expectedFiles)
	return result, nil
}

// checkFilesExist stats each expected file and records any that are missing
// or not a regular file, mutating result in place. Shared by VerifyBaseline
// and VerifyFull so the two verification levels agree on what "missing"
// means.
func checkFilesExist(result *VerificationResult, dir string, expectedFiles map[string]string) {
	for filename := range expectedFiles {
		path := filepath.Join(dir, filename)
		info, err := os.Stat(path)
		if err != nil {
			result.Valid = false
			result.MissingFiles = append(result.MissingFiles, filename)
			continue
		}
		if !info.Mode().IsRegular() {
			result.Valid = false
			result.MissingFiles = append(result.MissingFiles, filename)
		}
	}
}

// VerifyFull performs complete SHA256 verification of all files in dir
// against expectedFiles. This is expensive and used only by `llmtui doctor`
// or the installer's post-extraction verification.
//
// expectedFiles is a map of filename → expected SHA256.
func VerifyFull(dir string, expectedFiles map[string]string) (*VerificationResult, error) {
	result := &VerificationResult{Valid: true}
	checkFilesExist(result, dir, expectedFiles)

	for filename, expectedHash := range expectedFiles {
		if slices.Contains(result.MissingFiles, filename) {
			continue
		}
		actualHash, err := computeFileSHA256(filepath.Join(dir, filename))
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", filepath.Join(dir, filename), err)
		}
		if actualHash != expectedHash {
			result.Valid = false
			result.BadHashes = append(result.BadHashes, fmt.Sprintf("%s (expected %s, got %s)", filename, expectedHash, actualHash))
		}
	}

	return result, nil
}

// VerifyQuick performs a quick verification suitable for startup:
// checks baseline file presence/size, then hashes only libllama (the
// primary entry point) if full hashing would be too expensive.
//
// This is a middle ground between VerifyBaseline (no hashing) and
// VerifyFull (hash everything). Managed tiers use this at startup.
func VerifyQuick(dir string, expectedFiles map[string]string, expectedVersion string) (*VerificationResult, error) {
	return VerifyQuickForPlatform(dir, expectedFiles, expectedVersion, goruntime.GOOS)
}

// VerifyQuickForPlatform performs startup verification for an explicit OS.
func VerifyQuickForPlatform(dir string, expectedFiles map[string]string, expectedVersion, goos string) (*VerificationResult, error) {
	// First do baseline check
	result, err := VerifyBaseline(dir, expectedFiles, expectedVersion, false)
	if err != nil {
		return nil, err
	}
	if !result.Valid {
		return result, nil
	}

	// Hash only the primary library as a quick integrity check
	primaryLib := primaryLibraryName(goos)
	if expectedHash, ok := expectedFiles[primaryLib]; ok {
		path := filepath.Join(dir, primaryLib)
		actualHash, err := computeFileSHA256(path)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", path, err)
		}
		if actualHash != expectedHash {
			result.Valid = false
			result.BadHashes = append(result.BadHashes, fmt.Sprintf("%s (expected %s, got %s)", primaryLib, expectedHash, actualHash))
		}
	}

	return result, nil
}

// computeFileSHA256 returns the lowercase hex SHA256 of the file at path.
func computeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// getPrimaryLibraryName returns the platform-specific primary llama library name.
func getPrimaryLibraryName() string {
	return primaryLibraryName(goruntime.GOOS)
}
