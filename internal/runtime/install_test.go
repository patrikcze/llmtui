package runtime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/patrikcze/llmtui/internal/testutil"
)

// createTestArchive creates a test tar.gz archive with specified files.
func createTestArchive(t *testing.T, files map[string]string) (string, int64, string) {
	t.Helper()

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "test.tar.gz")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Write files to archive
	for filename, content := range files {
		// Put files in a nested directory to test extraction
		name := "nested-dir/" + filename
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("write header for %s: %v", filename, err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatalf("write content for %s: %v", filename, err)
		}
	}

	// Close writers to flush
	tw.Close()
	gzw.Close()
	f.Close()

	// Compute size and SHA256
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	h := sha256.Sum256(data)
	sha256Hex := hex.EncodeToString(h[:])

	return archivePath, int64(len(data)), sha256Hex
}

// createTestPin creates a minimal Pin for testing.
func createTestPin(t *testing.T, files map[string]string, aliases map[string]string, archiveURL string, archiveSize int64, archiveSHA256 string) *Pin {
	t.Helper()

	fileHashes := make(map[string]string)
	for filename, content := range files {
		h := sha256.Sum256([]byte(content))
		fileHashes[filename] = hex.EncodeToString(h[:])
	}

	return &Pin{
		YzmaVersion: "test-v1",
		LlamaTag:    "test-b1000",
		LlamaCommit: "test-commit",
		CompatibleRange: CompatibleRange{
			Min: "test-b1000",
			Max: "test-b1000",
		},
		Platforms: map[string]PlatformPin{
			"test-os-test-arch": {
				Archive: "test.tar.gz",
				URL:     archiveURL,
				SHA256:  archiveSHA256,
				Size:    archiveSize,
				Files:   fileHashes,
				Aliases: aliases,
			},
		},
	}
}

// TestInstall_Success tests successful installation.
func TestInstall_Success(t *testing.T) {
	// Create test files
	files := map[string]string{
		"libtest.so": "test library content",
		"test.dll":   "test dll content",
	}
	aliases := map[string]string{
		"libtest.so.1": "libtest.so",
	}

	// Create test archive
	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	// Create test HTTP server
	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	// Create test pin
	pin := createTestPin(t, files, aliases, ts.URL, archiveSize, archiveSHA256)

	// Override LoadPin for this test
	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	// Create test platform config
	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	// Install
	ctx := context.Background()
	result, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})

	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	if !result.Installed {
		t.Errorf("expected Installed=true, got false")
	}

	if result.Tag != "test-b1000" {
		t.Errorf("expected Tag=test-b1000, got %s", result.Tag)
	}

	// Verify files exist
	for filename := range files {
		path := filepath.Join(result.Dir, filename)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file %s not found: %v", filename, err)
		}
	}

	// Verify aliases
	for alias := range aliases {
		path := filepath.Join(result.Dir, alias)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("alias %s not found: %v", alias, err)
		}
	}

	// Verify manifest
	manifest, err := ReadManifest(result.Dir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.LlamaVersion != "test-b1000" {
		t.Errorf("expected LlamaVersion=test-b1000, got %s", manifest.LlamaVersion)
	}

	// Verify version stamp
	version, err := ReadVersion(result.Dir)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if !versionMatches(version, "test-b1000") {
		t.Errorf("expected version test-b1000, got %s", version)
	}
}

// TestInstall_ChecksumMismatch tests that installation fails on checksum mismatch.
func TestInstall_ChecksumMismatch(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, _ := createTestArchive(t, files)

	// Use wrong SHA256
	wrongSHA256 := strings.Repeat("0", 64)

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, wrongSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()
	_, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})

	if err == nil {
		t.Fatalf("expected error on checksum mismatch, got nil")
	}

	if !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Errorf("expected SHA256 mismatch error, got: %v", err)
	}
}

func TestInstall_RejectsWrongArchiveSize(t *testing.T) {
	files := map[string]string{"libtest.so": "test library content"}
	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, 0)
	server := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	pin := createTestPin(t, files, nil, server.URL, archiveSize, archiveSHA256)
	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	_, err = Install(context.Background(), InstallOptions{
		Platform: "test-os-test-arch",
		Dest:     filepath.Join(t.TempDir(), "runtime"),
		PlatformConfig: &PlatformConfig{
			GOOS: "test-os", GOARCH: "test-arch", Stat: os.Stat, EvalSymlinks: filepath.EvalSymlinks,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "archive size") {
		t.Fatalf("Install() error = %v, want archive size mismatch", err)
	}
}

func TestInstall_PreservesInvalidExistingDestination(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "user-data")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	pin := createTestPin(t, map[string]string{"libtest.so": "runtime"}, nil, "https://invalid.example", 1, strings.Repeat("0", 64))
	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	_, err := Install(context.Background(), InstallOptions{
		Platform: "test-os-test-arch",
		Dest:     dest,
		PlatformConfig: &PlatformConfig{
			GOOS: "test-os", GOARCH: "test-arch", Stat: os.Stat, EvalSymlinks: filepath.EvalSymlinks,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Install() error = %v, want existing destination refusal", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep" {
		t.Fatalf("existing destination was modified: data=%q error=%v", data, readErr)
	}
}

// TestInstall_TraversalRejection tests that path traversal in archives is rejected.
func TestInstall_TraversalRejection(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "evil.tar.gz")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	// Try to write a file with .. in the path
	evilContent := "evil content"
	if err := tw.WriteHeader(&tar.Header{
		Name: "../../../etc/passwd",
		Mode: 0644,
		Size: int64(len(evilContent)),
	}); err != nil {
		t.Fatalf("write evil header: %v", err)
	}
	io.WriteString(tw, evilContent)

	tw.Close()
	gzw.Close()
	f.Close()

	// Read archive for server
	data, _ := os.ReadFile(archivePath)
	h := sha256.Sum256(data)
	archiveSHA256 := hex.EncodeToString(h[:])

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	files := map[string]string{
		"libtest.so": "test",
	}
	pin := createTestPin(t, files, nil, ts.URL, int64(len(data)), archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()
	_, err = Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})

	if err == nil {
		t.Fatalf("expected error on traversal, got nil")
	}

	// Should fail because expected file not found in archive
	if !strings.Contains(err.Error(), "expected file not found") && !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("expected traversal or missing file error, got: %v", err)
	}
}

func TestExtractZipRejectsAllowlistedSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "runtime.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "nested/libtest.dll"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "../../outside"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = extractZipSafe(archive, filepath.Join(dir, "extract"), &PlatformPin{Files: map[string]string{"libtest.dll": "unused"}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("extractZipSafe() error = %v, want special-file rejection", err)
	}
}

func TestExtractArchiveEntryRejectsResourceBombBeforeOpen(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})

	tests := []struct {
		name      string
		size      int64
		total     int64
		wantError string
	}{
		{name: "per entry", size: maxRuntimeEntryBytes + 1, wantError: "extracted size"},
		{name: "total payload", size: 1, total: maxRuntimeExtractBytes, wantError: "total extraction limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := false
			total := tt.total
			err := extractArchiveEntry(root, tt.name, tt.name, true, tt.size, func() (io.ReadCloser, error) {
				opened = true
				return io.NopCloser(strings.NewReader("x")), nil
			}, map[string]bool{}, &total)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if opened {
				t.Fatal("oversized entry was opened before its metadata limit was enforced")
			}
		})
	}
}

func TestExtractZipRejectsDuplicateAllowlistedBasename(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "duplicate.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"first/libtest.dll", "second/libtest.dll"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, "same"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = extractZipSafe(archive, filepath.Join(dir, "extract"), &PlatformPin{Files: map[string]string{"libtest.dll": "unused"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate file") {
		t.Fatalf("error = %v, want duplicate-file rejection", err)
	}
}

// TestInstall_AlreadyInstalled tests that re-installing returns existing installation.
func TestInstall_AlreadyInstalled(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	downloadCount := 0
	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadCount++
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()

	// First install
	result1, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("first install failed: %v", err)
	}
	if !result1.Installed {
		t.Errorf("expected first install to be new")
	}

	// Second install should return existing
	result2, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("second install failed: %v", err)
	}
	if result2.Installed {
		t.Errorf("expected second install to return existing")
	}

	if downloadCount != 1 {
		t.Errorf("expected 1 download, got %d", downloadCount)
	}
}

func TestInstall_BackendVariant(t *testing.T) {
	files := map[string]string{
		"libtest.so":        "base library",
		"libtest-vulkan.so": "vulkan backend",
	}
	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)
	server := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		data, readErr := os.ReadFile(archivePath)
		if readErr != nil {
			t.Error(readErr)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()

	pin := createTestPin(t, map[string]string{"libtest.so": "base library"}, nil, "unused", 1, "unused")
	variantHash := sha256.Sum256([]byte("vulkan backend"))
	platformPin := pin.Platforms["test-os-test-arch"]
	platformPin.Packs = map[string]PackPin{
		"vulkan": {
			Archive: "test.tar.gz",
			URL:     server.URL,
			SHA256:  archiveSHA256,
			Size:    archiveSize,
			Files:   map[string]string{"libtest-vulkan.so": hex.EncodeToString(variantHash[:])},
		},
	}
	pin.Platforms["test-os-test-arch"] = platformPin
	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	dest := filepath.Join(t.TempDir(), "runtime")
	pc := &PlatformConfig{GOOS: "test-os", GOARCH: "test-arch", Stat: os.Stat, EvalSymlinks: filepath.EvalSymlinks}
	result, err := Install(context.Background(), InstallOptions{
		Platform: "test-os-test-arch", Backend: "vulkan", Dest: dest, PlatformConfig: pc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "libtest-vulkan.so")); err != nil {
		t.Fatalf("Vulkan module missing: %v", err)
	}
	manifest, err := ReadManifest(result.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Backend != "vulkan" {
		t.Fatalf("manifest backend = %q, want vulkan", manifest.Backend)
	}
}

// TestInstall_ConcurrentInstall tests that concurrent installs converge safely.
func TestInstall_ConcurrentInstall(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	var downloadCount int
	var mu sync.Mutex
	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		downloadCount++
		mu.Unlock()
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()

	// Launch 3 concurrent installs
	var wg sync.WaitGroup
	results := make([]*InstallResult, 3)
	errors := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errors[idx] = Install(ctx, InstallOptions{
				Platform:       "test-os-test-arch",
				PlatformConfig: pc,
			})
		}(i)
	}

	wg.Wait()

	// All should succeed
	installedCount := 0
	for i := 0; i < 3; i++ {
		if errors[i] != nil {
			t.Errorf("install %d failed: %v", i, errors[i])
		}
		if results[i].Installed {
			installedCount++
		}
	}

	// At least one should report installed=true, others may report false if they found existing
	if installedCount == 0 {
		t.Errorf("expected at least one install to succeed with Installed=true")
	}

	// Verify final installation is valid
	finalDir := results[0].Dir
	manifest, err := ReadManifest(finalDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.LlamaVersion != "test-b1000" {
		t.Errorf("expected LlamaVersion=test-b1000, got %s", manifest.LlamaVersion)
	}
}

// TestUninstall_Success tests successful uninstallation.
func TestUninstall_Success(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}
	aliases := map[string]string{
		"libtest.so.1": "libtest.so",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, aliases, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()

	// Install first
	result, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Verify installation exists
	if _, err := os.Stat(result.Dir); err != nil {
		t.Fatalf("install directory not found: %v", err)
	}

	// Uninstall
	err = Uninstall(UninstallOptions{
		Tag:            "test-b1000",
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(result.Dir); !os.IsNotExist(err) {
		t.Errorf("expected directory to be removed, but it still exists")
	}
}

// TestUninstall_RefusesExtraFiles tests that uninstall refuses extra files.
func TestUninstall_RefusesExtraFiles(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	destDir := t.TempDir()
	pc := &PlatformConfig{
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat:   os.Stat,
		DataDir: func() (string, error) {
			return destDir, nil
		},
		EvalSymlinks: filepath.EvalSymlinks,
	}

	ctx := context.Background()

	// Install first
	result, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Add extra file
	extraFile := filepath.Join(result.Dir, "extra.txt")
	if err := os.WriteFile(extraFile, []byte("extra"), 0644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	// Uninstall should fail
	err = Uninstall(UninstallOptions{
		Tag:            "test-b1000",
		Platform:       "test-os-test-arch",
		Force:          false,
		PlatformConfig: pc,
	})
	if err == nil {
		t.Fatalf("expected uninstall to fail with extra files")
	}
	if !strings.Contains(err.Error(), "unmanaged files") {
		t.Errorf("expected 'unmanaged files' error, got: %v", err)
	}

	// Uninstall with force should succeed
	err = Uninstall(UninstallOptions{
		Tag:            "test-b1000",
		Platform:       "test-os-test-arch",
		Force:          true,
		PlatformConfig: pc,
	})
	if err != nil {
		t.Fatalf("uninstall with force failed: %v", err)
	}
}

func TestUninstall_RejectsForgedManifest(t *testing.T) {
	root := t.TempDir()
	pc := &PlatformConfig{
		GOOS: "test-os", GOARCH: "test-arch", Stat: os.Stat,
		DataDir:      func() (string, error) { return root, nil },
		EvalSymlinks: filepath.EvalSymlinks,
	}
	dir, err := pc.ManagedRuntimeDir("test-b1000")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(filepath.Dir(dir), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteVersion(dir, "test-b1000"); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(dir, &Manifest{LlamaVersion: "test-b1000", Files: map[string]string{"../victim": "forged"}}); err != nil {
		t.Fatal(err)
	}
	pin := createTestPin(t, map[string]string{"libtest.so": "runtime"}, nil, "", 0, "")
	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	err = Uninstall(UninstallOptions{Tag: "test-b1000", Platform: "test-os-test-arch", PlatformConfig: pc})
	if err == nil || !strings.Contains(err.Error(), "manifest does not match") {
		t.Fatalf("Uninstall() error = %v, want forged manifest refusal", err)
	}
	if data, readErr := os.ReadFile(victim); readErr != nil || string(data) != "keep" {
		t.Fatalf("victim was modified: data=%q error=%v", data, readErr)
	}
}

// TestList tests the List function.
func TestList(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	// Mock CurrentPlatform
	oldCurrentPlatform := CurrentPlatform
	CurrentPlatform = func() string { return "test-os-test-arch" }
	defer func() { CurrentPlatform = oldCurrentPlatform }()

	destDir := t.TempDir()

	// Mock DefaultPlatformConfig
	oldDefaultPlatformConfig := DefaultPlatformConfig
	DefaultPlatformConfig = func() *PlatformConfig {
		return &PlatformConfig{
			GOOS:   "test-os",
			GOARCH: "test-arch",
			Stat:   os.Stat,
			DataDir: func() (string, error) {
				return destDir, nil
			},
			EvalSymlinks: filepath.EvalSymlinks,
		}
	}
	defer func() { DefaultPlatformConfig = oldDefaultPlatformConfig }()

	// List before install
	result, err := List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if result.Installed {
		t.Errorf("expected not installed")
	}

	// Install
	ctx := context.Background()
	_, err = Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: DefaultPlatformConfig(),
	})
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// List after install
	result, err = List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if !result.Installed {
		t.Errorf("expected installed")
	}
	if !result.Valid {
		t.Errorf("expected valid, got errors: %v", result.Errors)
	}
}

// TestVerify tests the Verify function.
func TestVerify(t *testing.T) {
	files := map[string]string{
		"libtest.so": "test library content",
	}

	archivePath, archiveSize, archiveSHA256 := createTestArchive(t, files)

	ts := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archivePath)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	pin := createTestPin(t, files, nil, ts.URL, archiveSize, archiveSHA256)

	oldLoadPin := LoadPin
	LoadPin = func() (*Pin, error) { return pin, nil }
	defer func() { LoadPin = oldLoadPin }()

	oldCurrentPlatform := CurrentPlatform
	CurrentPlatform = func() string { return "test-os-test-arch" }
	defer func() { CurrentPlatform = oldCurrentPlatform }()

	destDir := t.TempDir()

	oldDefaultPlatformConfig := DefaultPlatformConfig
	DefaultPlatformConfig = func() *PlatformConfig {
		return &PlatformConfig{
			GOOS:   "test-os",
			GOARCH: "test-arch",
			Stat:   os.Stat,
			DataDir: func() (string, error) {
				return destDir, nil
			},
			EvalSymlinks: filepath.EvalSymlinks,
		}
	}
	defer func() { DefaultPlatformConfig = oldDefaultPlatformConfig }()

	// Install
	ctx := context.Background()
	installResult, err := Install(ctx, InstallOptions{
		Platform:       "test-os-test-arch",
		PlatformConfig: DefaultPlatformConfig(),
	})
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	// Verify should pass
	result, err := Verify()
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !result.Valid {
		t.Errorf("expected valid=true")
	}

	// Corrupt a file
	corruptPath := filepath.Join(installResult.Dir, "libtest.so")
	if err := os.WriteFile(corruptPath, []byte("corrupted"), 0644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	// Verify should fail
	result, err = Verify()
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Valid {
		t.Errorf("expected valid=false after corruption")
	}
	if len(result.Result.BadHashes) == 0 {
		t.Errorf("expected bad hash report")
	}
}
