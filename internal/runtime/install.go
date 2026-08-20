package runtime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// InstallOptions configures a runtime installation.
type InstallOptions struct {
	// Platform override (defaults to CurrentPlatform)
	Platform string

	// Backend selects the pinned runtime variant (defaults to cpu).
	Backend string

	// Dest override for tests/advanced use (defaults to ManagedRuntimeDir)
	Dest string

	// HTTPClient for downloads (defaults to standard client with timeouts)
	HTTPClient *http.Client

	// PlatformConfig for filesystem operations (defaults to DefaultPlatformConfig)
	PlatformConfig *PlatformConfig
}

// InstallResult records the outcome of an installation.
type InstallResult struct {
	Dir       string
	Tag       string
	Platform  string
	Installed bool // true if newly installed, false if already valid
}

// Install downloads, verifies, and extracts a llama.cpp runtime for the
// current platform. It performs atomic installation with full integrity
// checks, creates trusted symlink aliases, and writes the manifest.
//
// Network access occurs only during this call. Returns an error if download
// fails, checksums mismatch, or extraction fails. Concurrent installs to the
// same destination converge safely or return an already-installed result.
func Install(ctx context.Context, opts InstallOptions) (*InstallResult, error) {
	// Load embedded pin
	pin, err := LoadPin()
	if err != nil {
		return nil, fmt.Errorf("load embedded pin: %w", err)
	}

	// Resolve platform
	platform := opts.Platform
	if platform == "" {
		platform = CurrentPlatform()
	}

	platformPin, err := pin.PlatformPin(platform)
	if err != nil {
		return nil, err
	}
	backend := opts.Backend
	if backend == "" {
		backend = "cpu"
	}
	platformPin, err = platformPin.ForBackend(backend)
	if err != nil {
		return nil, err
	}

	// Resolve destination
	pc := opts.PlatformConfig
	if pc == nil {
		pc = DefaultPlatformConfig()
	}

	dest := opts.Dest
	if dest == "" {
		dest, err = pc.ManagedRuntimeDir(pin.LlamaTag)
		if err != nil {
			return nil, fmt.Errorf("resolve managed runtime dir: %w", err)
		}
	}

	// Check if already installed and valid
	if isValidInstallation(dest, pin.LlamaTag, platformPin, pc) {
		return &InstallResult{
			Dir:       dest,
			Tag:       pin.LlamaTag,
			Platform:  platform,
			Installed: false,
		}, nil
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		if existing, mErr := ReadManifest(dest); mErr == nil && manifestBackend(existing) != backend {
			return nil, fmt.Errorf(
				"runtime destination %q already holds a %q-backend installation; run `llmtui runtime uninstall` first, or pass --dest to install the %q backend to a different location",
				dest, manifestBackend(existing), backend,
			)
		}
		return nil, fmt.Errorf("runtime destination %q already exists but is not a valid %s installation; move or remove it explicitly", dest, pin.LlamaTag)
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect runtime destination %q: %w", dest, statErr)
	}

	// Prepare HTTP client
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Minute,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		}
	}

	// Create staging directory in parent with restrictive permissions
	parentDir := filepath.Dir(dest)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("create parent directory: %w", err)
	}

	stagingDir, err := os.MkdirTemp(parentDir, ".llmtui-install-*")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir) // Cleanup on failure

	// Set restrictive permissions on staging directory
	if err := os.Chmod(stagingDir, 0700); err != nil {
		return nil, fmt.Errorf("set staging directory permissions: %w", err)
	}

	// Download archive
	archivePath := filepath.Join(stagingDir, platformPin.Archive)
	cachePath, cacheErr := runtimeArchiveCachePath(platformPin.Archive)
	cacheHit := cacheErr == nil && verifyArchiveFile(cachePath, platformPin.Size, platformPin.SHA256) == nil
	if cacheHit {
		if err := copyFile(cachePath, archivePath); err != nil {
			return nil, fmt.Errorf("copy cached archive: %w", err)
		}
	} else {
		if err := downloadArchive(ctx, client, platformPin.URL, archivePath, platformPin.Size, platformPin.SHA256); err != nil {
			return nil, fmt.Errorf("download archive: %w", err)
		}
		if cacheErr == nil {
			_ = cacheArchive(archivePath, cachePath)
		}
	}

	// Extract archive with safety checks
	extractDir := filepath.Join(stagingDir, "extract")
	if err := os.Mkdir(extractDir, 0755); err != nil {
		return nil, fmt.Errorf("create extract directory: %w", err)
	}

	if err := extractArchiveSafe(archivePath, extractDir, platformPin); err != nil {
		return nil, fmt.Errorf("extract archive: %w", err)
	}

	// Verify extracted files
	if err := verifyExtractedFiles(extractDir, platformPin); err != nil {
		return nil, fmt.Errorf("verify extracted files: %w", err)
	}

	// Some official platform archives (the Windows CPU/Vulkan zips, as of
	// b10066) don't include a LICENSE file at all, unlike the macOS/Linux
	// tar.gz archives. Guarantee one is always present in the managed
	// directory using llmtui's own verified copy of the same upstream text.
	if err := ensureLicensePresent(extractDir); err != nil {
		return nil, fmt.Errorf("write fallback license: %w", err)
	}

	// Create aliases (symlinks on Unix, copies on Windows)
	if err := createAliases(extractDir, platformPin.Aliases, pc.GOOS); err != nil {
		return nil, fmt.Errorf("create aliases: %w", err)
	}

	// Write manifest and stamps
	manifest, err := NewManifestForBackend(pin, platform, backend)
	if err != nil {
		return nil, fmt.Errorf("create manifest: %w", err)
	}

	if err := WriteManifest(extractDir, manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	expectedFiles, err := platformPin.ManagedFiles()
	if err != nil {
		return nil, fmt.Errorf("resolve managed runtime files: %w", err)
	}
	verification, err := VerifyFull(extractDir, expectedFiles)
	if err != nil {
		return nil, fmt.Errorf("verify staged runtime: %w", err)
	}
	if !verification.Valid {
		return nil, fmt.Errorf("verify staged runtime: %s", verificationSummary(verification))
	}

	// Fsync directory for durability; failure is non-fatal and intentionally
	// ignored — the subsequent atomic rename is still correct without it.
	_ = syncDirectory(extractDir)

	if err := os.Rename(extractDir, dest); err != nil {
		if isValidInstallation(dest, pin.LlamaTag, platformPin, pc) {
			return &InstallResult{Dir: dest, Tag: pin.LlamaTag, Platform: platform}, nil
		}
		return nil, fmt.Errorf("move to final destination: %w", err)
	}

	return &InstallResult{
		Dir:       dest,
		Tag:       pin.LlamaTag,
		Platform:  platform,
		Installed: true,
	}, nil
}

// downloadArchive downloads url to path, verifying size and SHA256.
func downloadArchive(ctx context.Context, client *http.Client, url, path string, expectedSize int64, expectedSHA256 string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d %s", url, resp.StatusCode, resp.Status)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != expectedSize {
		return fmt.Errorf("archive size %d does not match expected %d", resp.ContentLength, expectedSize)
	}

	// Create temporary file
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	limited := io.LimitReader(resp.Body, expectedSize+1)
	n, err := io.Copy(io.MultiWriter(f, h), limited)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if n != expectedSize {
		return fmt.Errorf("archive size %d does not match expected %d", n, expectedSize)
	}

	actualSHA256 := hex.EncodeToString(h.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedSHA256, actualSHA256)
	}

	return nil
}

func runtimeArchiveCachePath(archive string) (string, error) {
	if filepath.Base(archive) != archive {
		return "", fmt.Errorf("invalid runtime archive name %q", archive)
	}
	cacheDir := os.Getenv("LLMTUI_RUNTIME_CACHE")
	if cacheDir == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		cacheDir = filepath.Join(userCache, "llmtui", "runtime-archives")
	}
	return filepath.Join(cacheDir, archive), nil
}

func verifyArchiveFile(filename string, expectedSize int64, expectedSHA256 string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return err
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("archive size %d does not match expected %d", info.Size(), expectedSize)
	}
	actual, err := computeFileSHA256(filename)
	if err != nil {
		return err
	}
	if actual != expectedSHA256 {
		return fmt.Errorf("archive SHA256 mismatch: expected %s, got %s", expectedSHA256, actual)
	}
	return nil
}

func cacheArchive(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".runtime-archive-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if closeErr := temp.Close(); closeErr != nil {
		_ = os.Remove(tempName)
		return closeErr
	}
	defer os.Remove(tempName)
	if err := copyFile(source, tempName); err != nil {
		return err
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempName, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// extractArchiveSafe extracts the archive to extractDir, rejecting traversal,
// absolute paths, and special files. Extracts only PlatformPin.Files by basename.
func extractArchiveSafe(archivePath, extractDir string, platformPin *PlatformPin) error {
	// Determine archive type
	if strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
		return extractTarGzSafe(archivePath, extractDir, platformPin)
	} else if strings.HasSuffix(archivePath, ".zip") {
		return extractZipSafe(archivePath, extractDir, platformPin)
	}
	return fmt.Errorf("unsupported archive format: %s", archivePath)
}

// extractTarGzSafe extracts a tar.gz archive safely.
func extractTarGzSafe(archivePath, extractDir string, platformPin *PlatformPin) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	allowlist := archiveAllowlist(platformPin)
	extracted := make(map[string]bool)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if err := validateArchivePath(header.Name); err != nil {
			return err
		}
		basename := path.Base(strings.ReplaceAll(header.Name, "\\", "/"))
		if !allowlist[basename] {
			continue
		}
		open := func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }
		if err := extractArchiveEntry(extractDir, header.Name, basename, header.Typeflag == tar.TypeReg, open, extracted); err != nil {
			return err
		}
	}

	return verifyAllExtracted(allowlist, extracted)
}

// extractZipSafe extracts a zip archive safely.
func extractZipSafe(archivePath, extractDir string, platformPin *PlatformPin) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	allowlist := archiveAllowlist(platformPin)
	extracted := make(map[string]bool)

	for _, entry := range r.File {
		if err := validateArchivePath(entry.Name); err != nil {
			return err
		}
		basename := path.Base(strings.ReplaceAll(entry.Name, "\\", "/"))
		if !allowlist[basename] {
			continue
		}
		isRegular := entry.Mode()&os.ModeType == 0 && entry.Mode().IsRegular()
		if err := extractArchiveEntry(extractDir, entry.Name, basename, isRegular, entry.Open, extracted); err != nil {
			return err
		}
	}

	return verifyAllExtracted(allowlist, extracted)
}

// archiveAllowlist returns the set of basenames extraction is permitted to
// write, derived from the platform pin's expected file list.
func archiveAllowlist(platformPin *PlatformPin) map[string]bool {
	allowlist := make(map[string]bool, len(platformPin.Files))
	for filename := range platformPin.Files {
		allowlist[filename] = true
	}
	return allowlist
}

// extractArchiveEntry writes one already-allowlisted archive entry (basename)
// to extractDir, shared by both the tar.gz and zip extraction paths so a
// safety fix (duplicate rejection, non-regular-file rejection) applies to
// both formats identically. name is the raw archive-internal path, used only
// for error messages.
func extractArchiveEntry(extractDir, name, basename string, isRegular bool, open func() (io.ReadCloser, error), extracted map[string]bool) error {
	if !isRegular {
		return fmt.Errorf("allowlisted archive entry %q is not a regular file", name)
	}
	if extracted[basename] {
		return fmt.Errorf("duplicate file in archive: %s", basename)
	}

	rc, err := open()
	if err != nil {
		return fmt.Errorf("open file in archive %s: %w", basename, err)
	}
	defer rc.Close()

	outFile, err := os.OpenFile(filepath.Join(extractDir, basename), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create file %s: %w", basename, err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("extract file %s: %w", basename, err)
	}

	extracted[basename] = true
	return nil
}

// verifyAllExtracted confirms every allowlisted file was actually present in
// the archive.
func verifyAllExtracted(allowlist, extracted map[string]bool) error {
	for filename := range allowlist {
		if !extracted[filename] {
			return fmt.Errorf("expected file not found in archive: %s", filename)
		}
	}
	return nil
}

func validateArchivePath(name string) error {
	normalized := strings.ReplaceAll(name, "\\", "/")
	cleaned := path.Clean(normalized)
	if normalized == "" || strings.HasPrefix(normalized, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("archive contains unsafe path %q", name)
	}
	return nil
}

// verifyExtractedFiles performs full SHA256 verification of extracted files.
func verifyExtractedFiles(extractDir string, platformPin *PlatformPin) error {
	for filename, expectedHash := range platformPin.Files {
		path := filepath.Join(extractDir, filename)

		actualHash, err := computeFileSHA256(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", filename, err)
		}

		if actualHash != expectedHash {
			return fmt.Errorf("%s: SHA256 mismatch (expected %s, got %s)", filename, expectedHash, actualHash)
		}
	}
	return nil
}

// ensureLicensePresent writes llmtui's embedded copy of llama.cpp's LICENSE
// into extractDir if the extracted archive didn't already provide one. It
// never overwrites a LICENSE that came from the archive itself (that copy
// was already SHA256-verified against the pin in verifyExtractedFiles).
func ensureLicensePresent(extractDir string) error {
	path := filepath.Join(extractDir, "LICENSE")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return os.WriteFile(path, embeddedLlamaCppLicense, 0644)
}

// createAliases creates trusted symlinks (Unix) or copies (Windows) for aliases.
func createAliases(dir string, aliases map[string]string, goos string) error {
	for alias, target := range aliases {
		aliasPath := filepath.Join(dir, alias)
		targetPath := filepath.Join(dir, target)

		// Verify target exists
		if _, err := os.Stat(targetPath); err != nil {
			return fmt.Errorf("alias %s references nonexistent target %s", alias, target)
		}

		if goos == "windows" {
			// Copy target to alias
			if err := copyFile(targetPath, aliasPath); err != nil {
				return fmt.Errorf("copy alias %s: %w", alias, err)
			}
		} else {
			// Create symlink
			if err := os.Symlink(target, aliasPath); err != nil {
				return fmt.Errorf("create symlink %s: %w", alias, err)
			}
		}
	}
	return nil
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

// syncDirectory syncs a directory for durability (best effort).
func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// isValidInstallation checks if dir contains a valid installation.
func isValidInstallation(dir, expectedTag string, platformPin *PlatformPin, pc *PlatformConfig) bool {
	// Check if directory exists
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	secure, err := pc.IsSecureOwnership(dir)
	if err != nil || !secure {
		return false
	}

	// Check version stamp
	version, err := ReadVersion(dir)
	if err != nil || !versionMatches(version, expectedTag) {
		return false
	}

	// Check manifest
	manifest, err := ReadManifest(dir)
	if err != nil {
		return false
	}

	expectedFiles, err := platformPin.ManagedFiles()
	if err != nil {
		return false
	}

	if manifest.LlamaVersion != expectedTag || !equalHashes(manifest.Files, expectedFiles) {
		return false
	}

	// Quick verification
	result, err := VerifyQuickForPlatform(dir, expectedFiles, expectedTag, pc.GOOS)
	if err != nil || !result.Valid {
		return false
	}

	return true
}

// UninstallOptions configures runtime uninstallation.
type UninstallOptions struct {
	// Tag to uninstall (defaults to current pin tag)
	Tag string

	// Platform override (defaults to CurrentPlatform)
	Platform string

	// Force removal even if extra files exist
	Force bool

	// PlatformConfig for filesystem operations
	PlatformConfig *PlatformConfig
}

// Uninstall removes a managed runtime directory. It reads the manifest and
// removes only manifest-listed files, aliases, and metadata. Refuses to remove
// the directory if extra files exist unless Force is true.
func Uninstall(opts UninstallOptions) error {
	// Load embedded pin
	pin, err := LoadPin()
	if err != nil {
		return fmt.Errorf("load embedded pin: %w", err)
	}

	tag := opts.Tag
	if tag == "" {
		tag = pin.LlamaTag
	}

	pc := opts.PlatformConfig
	if pc == nil {
		pc = DefaultPlatformConfig()
	}

	// Resolve managed runtime directory
	dir, err := pc.ManagedRuntimeDir(tag)
	if err != nil {
		return fmt.Errorf("resolve managed runtime dir: %w", err)
	}

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("runtime %s not installed at %s", tag, dir)
	}

	// Read manifest
	manifest, err := ReadManifest(dir)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	// Verify manifest matches expected tag
	if manifest.LlamaVersion != tag {
		return fmt.Errorf("manifest version %s does not match expected %s", manifest.LlamaVersion, tag)
	}
	version, err := ReadVersion(dir)
	if err != nil || !versionMatches(version, tag) {
		return fmt.Errorf("runtime version stamp does not match %s", tag)
	}

	// Resolve platform
	platform := opts.Platform
	if platform == "" {
		platform = CurrentPlatform()
	}

	// Get platform pin for aliases
	platformPin, err := pin.PlatformPin(platform)
	if err != nil {
		return fmt.Errorf("get platform pin: %w", err)
	}
	platformPin, err = platformPin.ForBackend(manifestBackend(manifest))
	if err != nil {
		return fmt.Errorf("select runtime backend: %w", err)
	}

	expectedFiles, err := platformPin.ManagedFiles()
	if err != nil {
		return fmt.Errorf("resolve managed runtime files: %w", err)
	}
	if !equalHashes(manifest.Files, expectedFiles) {
		return errors.New("runtime manifest does not match the embedded pin; refusing to remove files")
	}
	secure, err := pc.IsSecureOwnership(dir)
	if err != nil || !secure {
		return errors.New("runtime directory is not owner-controlled; refusing to remove files")
	}

	managedFiles := make(map[string]bool, len(expectedFiles)+2)
	for filename := range expectedFiles {
		if filepath.Base(filename) != filename {
			return fmt.Errorf("invalid managed runtime filename %q", filename)
		}
		managedFiles[filename] = true
	}
	managedFiles[ManifestFilename] = true
	managedFiles[VersionFilename] = true

	// Check for extra files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory: %w", err)
	}

	var extraFiles []string
	for _, entry := range entries {
		if !managedFiles[entry.Name()] {
			extraFiles = append(extraFiles, entry.Name())
		}
	}
	sort.Strings(extraFiles)

	if len(extraFiles) > 0 && !opts.Force {
		return fmt.Errorf("runtime directory contains unmanaged files: %v; move them before uninstalling", extraFiles)
	}

	// Remove managed files
	for filename := range managedFiles {
		path := filepath.Join(dir, filename)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filename, err)
		}
	}

	// Remove directory if empty
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		if opts.Force && len(extraFiles) > 0 {
			return nil
		}
		return fmt.Errorf("remove runtime directory: %w", err)
	}

	return nil
}

// ListResult describes an installed runtime.
type ListResult struct {
	Tag       string
	Platform  string
	Backend   string
	Dir       string
	Installed bool
	Valid     bool
	Errors    []string
}

// List returns information about the current platform's runtime installation.
func List() (*ListResult, error) {
	pin, err := LoadPin()
	if err != nil {
		return nil, fmt.Errorf("load embedded pin: %w", err)
	}

	platform := CurrentPlatform()
	platformPin, err := pin.GetPlatformPin()
	if err != nil {
		return nil, err
	}

	pc := DefaultPlatformConfig()
	dir, err := pc.ManagedRuntimeDir(pin.LlamaTag)
	if err != nil {
		return nil, fmt.Errorf("resolve managed runtime dir: %w", err)
	}

	result := &ListResult{
		Tag:      pin.LlamaTag,
		Platform: platform,
		Dir:      dir,
	}

	// Check if installed
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		result.Installed = false
		return result, nil
	}
	result.Installed = true
	manifest, manifestErr := ReadManifest(dir)
	if manifestErr == nil {
		result.Backend = manifestBackend(manifest)
		platformPin, err = platformPin.ForBackend(result.Backend)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			return result, nil
		}
	}

	// Validate installation
	expectedFiles, err := platformPin.ManagedFiles()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get expected files: %v", err))
		return result, nil
	}

	if isValidInstallation(dir, pin.LlamaTag, platformPin, pc) {
		result.Valid = true
	} else {
		result.Valid = false
		// Try to get more specific errors
		if version, err := ReadVersion(dir); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("version stamp: %v", err))
		} else if !versionMatches(version, pin.LlamaTag) {
			result.Errors = append(result.Errors, fmt.Sprintf("version mismatch: %s != %s", version, pin.LlamaTag))
		}

		if _, err := ReadManifest(dir); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("manifest: %v", err))
		}

		if verifyResult, err := VerifyQuickForPlatform(dir, expectedFiles, pin.LlamaTag, pc.GOOS); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("verification: %v", err))
		} else if !verifyResult.Valid {
			result.Errors = append(result.Errors, verificationSummary(verifyResult))
		}
	}

	return result, nil
}

// VerifyResult describes the outcome of verifying a runtime installation.
type VerifyResult struct {
	Tag      string
	Platform string
	Backend  string
	Dir      string
	Valid    bool
	Result   *VerificationResult
}

// Verify performs full SHA256 verification of a managed runtime installation.
func Verify() (*VerifyResult, error) {
	pin, err := LoadPin()
	if err != nil {
		return nil, fmt.Errorf("load embedded pin: %w", err)
	}

	platform := CurrentPlatform()
	platformPin, err := pin.GetPlatformPin()
	if err != nil {
		return nil, err
	}

	pc := DefaultPlatformConfig()
	dir, err := pc.ManagedRuntimeDir(pin.LlamaTag)
	if err != nil {
		return nil, fmt.Errorf("resolve managed runtime dir: %w", err)
	}

	result := &VerifyResult{
		Tag:      pin.LlamaTag,
		Platform: platform,
		Dir:      dir,
	}

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, fmt.Errorf("runtime not installed at %s", dir)
	}
	manifest, err := ReadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("read runtime manifest: %w", err)
	}
	result.Backend = manifestBackend(manifest)
	platformPin, err = platformPin.ForBackend(result.Backend)
	if err != nil {
		return nil, err
	}

	// Perform full verification
	expectedFiles, err := platformPin.ManagedFiles()
	if err != nil {
		return nil, fmt.Errorf("get expected files: %w", err)
	}

	verifyResult, err := VerifyFull(dir, expectedFiles)
	if err != nil {
		return nil, fmt.Errorf("verify files: %w", err)
	}

	result.Valid = verifyResult.Valid
	result.Result = verifyResult

	return result, nil
}
