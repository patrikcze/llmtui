package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPlatformConfig(t *testing.T) {
	pc := DefaultPlatformConfig()
	if pc == nil {
		t.Fatal("DefaultPlatformConfig() returned nil")
	}

	// Verify all functions are set
	if pc.ExecutablePath == nil {
		t.Error("ExecutablePath is nil")
	}
	if pc.HomeDir == nil {
		t.Error("HomeDir is nil")
	}
	if pc.DataDir == nil {
		t.Error("DataDir is nil")
	}
	if pc.GOOS == "" {
		t.Error("GOOS is empty")
	}
	if pc.GOARCH == "" {
		t.Error("GOARCH is empty")
	}
	if pc.Stat == nil {
		t.Error("Stat is nil")
	}
	if pc.EvalSymlinks == nil {
		t.Error("EvalSymlinks is nil")
	}
}

func TestExecutableRelativePaths(t *testing.T) {
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "bin", "llmtui")

	pc := &PlatformConfig{
		ExecutablePath: func() (string, error) { return exePath, nil },
		EvalSymlinks:   func(s string) (string, error) { return s, nil },
	}

	paths, err := pc.ExecutableRelativePaths()
	if err != nil {
		t.Fatalf("ExecutableRelativePaths() error = %v", err)
	}

	if len(paths) != 2 {
		t.Errorf("ExecutableRelativePaths() returned %d paths, want 2", len(paths))
	}

	expectedPaths := []string{
		filepath.Join(tmpDir, "bin", "lib", "llmtui", "runtime"),
		filepath.Join(tmpDir, "lib", "llmtui", "runtime"),
	}

	for i, expected := range expectedPaths {
		if paths[i] != expected {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], expected)
		}
	}
}

func TestExecutableRelativePaths_SymlinkResolution(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real", "bin", "llmtui")
	linkPath := filepath.Join(tmpDir, "link", "llmtui")

	pc := &PlatformConfig{
		ExecutablePath: func() (string, error) { return linkPath, nil },
		EvalSymlinks: func(s string) (string, error) {
			if s == linkPath {
				return realPath, nil
			}
			return s, nil
		},
	}

	paths, err := pc.ExecutableRelativePaths()
	if err != nil {
		t.Fatalf("ExecutableRelativePaths() error = %v", err)
	}

	// Should be relative to the resolved path, not the link
	if !filepath.HasPrefix(paths[0], filepath.Join(tmpDir, "real")) {
		t.Errorf("Paths not relative to resolved symlink: %v", paths)
	}
}

func TestExecutableRelativePaths_Error(t *testing.T) {
	pc := &PlatformConfig{
		ExecutablePath: func() (string, error) { return "", errors.New("mock error") },
	}

	_, err := pc.ExecutableRelativePaths()
	if err == nil {
		t.Error("ExecutableRelativePaths() expected error, got nil")
	}
}

func TestManagedRuntimeDir(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")

	pc := &PlatformConfig{
		DataDir: func() (string, error) { return dataDir, nil },
	}

	dir, err := pc.ManagedRuntimeDir("b10066")
	if err != nil {
		t.Fatalf("ManagedRuntimeDir() error = %v", err)
	}

	expected := filepath.Join(dataDir, "llmtui", "runtime", "b10066")
	if dir != expected {
		t.Errorf("ManagedRuntimeDir() = %q, want %q", dir, expected)
	}
}

func TestLegacyRuntimeDir(t *testing.T) {
	tmpDir := t.TempDir()

	pc := &PlatformConfig{
		HomeDir: func() (string, error) { return tmpDir, nil },
	}

	dir, err := pc.LegacyRuntimeDir()
	if err != nil {
		t.Fatalf("LegacyRuntimeDir() error = %v", err)
	}

	expected := filepath.Join(tmpDir, ".local", "share", "llmtui", "llama.cpp")
	if dir != expected {
		t.Errorf("LegacyRuntimeDir() = %q, want %q", dir, expected)
	}
}

func TestIsSecureOwnership_Windows(t *testing.T) {
	pc := &PlatformConfig{
		GOOS: "windows",
	}

	// On Windows, should always return true
	secure, err := pc.IsSecureOwnership("/any/path")
	if err != nil {
		t.Fatalf("IsSecureOwnership() error = %v", err)
	}
	if !secure {
		t.Error("IsSecureOwnership() on Windows should return true")
	}
}

func TestIsSecureOwnership_Unix_Secure(t *testing.T) {
	if CurrentPlatform() == "windows-amd64" {
		t.Skip("Unix permission test, skipping on Windows")
	}

	tmpDir := t.TempDir()
	securePath := filepath.Join(tmpDir, "secure")
	if err := os.Mkdir(securePath, 0755); err != nil {
		t.Fatalf("Setup: failed to create directory: %v", err)
	}

	pc := DefaultPlatformConfig()
	secure, err := pc.IsSecureOwnership(securePath)
	if err != nil {
		t.Fatalf("IsSecureOwnership() error = %v", err)
	}
	if !secure {
		t.Error("IsSecureOwnership() = false for 0755, want true")
	}
}

func TestIsSecureOwnership_Unix_Insecure(t *testing.T) {
	if CurrentPlatform() == "windows-amd64" {
		t.Skip("Unix permission test, skipping on Windows")
	}

	tmpDir := t.TempDir()
	insecurePath := filepath.Join(tmpDir, "insecure")
	if err := os.Mkdir(insecurePath, 0777); err != nil {
		t.Fatalf("Setup: failed to create directory: %v", err)
	}
	if err := os.Chmod(insecurePath, 0777); err != nil {
		t.Fatalf("Setup: failed to chmod directory: %v", err)
	}

	pc := DefaultPlatformConfig()
	secure, err := pc.IsSecureOwnership(insecurePath)
	if err != nil {
		t.Fatalf("IsSecureOwnership() error = %v", err)
	}
	if secure {
		t.Error("IsSecureOwnership() = true for 0777, want false")
	}
}

func TestIsSecureOwnership_Unix_GroupWritable(t *testing.T) {
	if CurrentPlatform() == "windows-amd64" {
		t.Skip("Unix permission test, skipping on Windows")
	}

	tmpDir := t.TempDir()
	groupWritablePath := filepath.Join(tmpDir, "groupwritable")
	if err := os.Mkdir(groupWritablePath, 0775); err != nil {
		t.Fatalf("Setup: failed to create directory: %v", err)
	}
	if err := os.Chmod(groupWritablePath, 0775); err != nil {
		t.Fatalf("Setup: failed to chmod directory: %v", err)
	}

	pc := DefaultPlatformConfig()
	secure, err := pc.IsSecureOwnership(groupWritablePath)
	if err != nil {
		t.Fatalf("IsSecureOwnership() error = %v", err)
	}
	if secure {
		t.Error("IsSecureOwnership() = true for 0775 (group-writable), want false")
	}
}

func TestDefaultDataDir(t *testing.T) {
	dir, err := defaultDataDir()
	if err != nil {
		// Error is acceptable on some systems
		t.Logf("defaultDataDir() error = %v (may be expected)", err)
		return
	}

	if dir == "" {
		t.Error("defaultDataDir() returned empty string")
	}

	// Should be an absolute path
	if !filepath.IsAbs(dir) {
		t.Errorf("defaultDataDir() = %q, want absolute path", dir)
	}
}

func TestPlatformConfigDependencyInjection(t *testing.T) {
	// Test that we can override all dependencies for tests
	called := false
	pc := &PlatformConfig{
		ExecutablePath: func() (string, error) {
			called = true
			return "/mock/exe", nil
		},
		HomeDir: func() (string, error) {
			return "/mock/home", nil
		},
		DataDir: func() (string, error) {
			return "/mock/data", nil
		},
		GOOS:   "test-os",
		GOARCH: "test-arch",
		Stat: func(path string) (os.FileInfo, error) {
			return nil, nil
		},
		EvalSymlinks: func(path string) (string, error) {
			return path, nil
		},
	}

	// Call a method that uses ExecutablePath
	_, _ = pc.ExecutableRelativePaths()
	if !called {
		t.Error("Injected ExecutablePath was not called")
	}

	// Verify other injections
	if pc.GOOS != "test-os" {
		t.Errorf("GOOS = %q, want test-os", pc.GOOS)
	}
	if pc.GOARCH != "test-arch" {
		t.Errorf("GOARCH = %q, want test-arch", pc.GOARCH)
	}
}
