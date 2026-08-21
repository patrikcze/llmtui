package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
)

// PlatformConfig provides dependency injection points for tests.
// All methods default to real OS calls if not overridden.
type PlatformConfig struct {
	// ExecutablePath returns the absolute path to the running executable.
	// Defaults to os.Executable.
	ExecutablePath func() (string, error)

	// HomeDir returns the user's home directory.
	// Defaults to os.UserHomeDir.
	HomeDir func() (string, error)

	// DataDir returns the user's XDG_DATA_HOME (or equivalent) directory.
	// Defaults to platform-specific data directory logic.
	DataDir func() (string, error)

	// GOOS and GOARCH override runtime.GOOS/GOARCH for tests.
	GOOS   string
	GOARCH string

	// Stat overrides os.Stat for tests.
	Stat func(string) (os.FileInfo, error)

	// EvalSymlinks overrides filepath.EvalSymlinks for tests.
	EvalSymlinks func(string) (string, error)
}

// DefaultPlatformConfig returns a PlatformConfig with all real OS calls.
var DefaultPlatformConfig = func() *PlatformConfig {
	return &PlatformConfig{
		ExecutablePath: os.Executable,
		HomeDir:        os.UserHomeDir,
		DataDir:        func() (string, error) { return defaultDataDir(goruntime.GOOS) },
		GOOS:           goruntime.GOOS,
		GOARCH:         goruntime.GOARCH,
		Stat:           os.Stat,
		EvalSymlinks:   filepath.EvalSymlinks,
	}
}

// defaultDataDir returns the platform-specific user data directory for goos.
// On macOS, Linux, and Android, this is XDG_DATA_HOME or ~/.local/share (on
// Android/Termux, $HOME is Termux's own home directory, so this resolves
// correctly with no special-casing). On Windows, this is %LOCALAPPDATA%.
// goos is an explicit parameter (rather than reading goruntime.GOOS
// internally) so tests can exercise every branch without a build-tag matrix.
func defaultDataDir(goos string) (string, error) {
	switch goos {
	case "darwin", "linux", "android":
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return xdg, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return localAppData, nil
		}
		return "", errors.New("LOCALAPPDATA not set")
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
}

// ExecutableRelativePaths returns candidate executable-relative runtime
// directories: <exe>/lib/llmtui/runtime and <exe>/../lib/llmtui/runtime.
// Symlinks in the executable path are resolved before computing candidates.
func (pc *PlatformConfig) ExecutableRelativePaths() ([]string, error) {
	exePath, err := pc.ExecutablePath()
	if err != nil {
		return nil, fmt.Errorf("get executable path: %w", err)
	}

	// Resolve symlinks
	exePath, err = pc.EvalSymlinks(exePath)
	if err != nil {
		return nil, fmt.Errorf("resolve executable symlinks: %w", err)
	}

	exeDir := filepath.Dir(exePath)
	return []string{
		filepath.Join(exeDir, "lib", "llmtui", "runtime"),
		filepath.Join(exeDir, "..", "lib", "llmtui", "runtime"),
	}, nil
}

// ManagedRuntimeDir returns the user data runtime directory for the given
// llama.cpp tag (e.g., XDG_DATA_HOME/llmtui/runtime/b10066).
func (pc *PlatformConfig) ManagedRuntimeDir(llamaTag string) (string, error) {
	dataDir, err := pc.DataDir()
	if err != nil {
		return "", fmt.Errorf("get data directory: %w", err)
	}
	return filepath.Join(dataDir, "llmtui", "runtime", llamaTag), nil
}

// LegacyRuntimeDir returns the legacy ~/.local/share/llmtui/llama.cpp directory.
// This is tier 5 and is accepted only if LLAMA_VERSION matches.
func (pc *PlatformConfig) LegacyRuntimeDir() (string, error) {
	home, err := pc.HomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "llmtui", "llama.cpp"), nil
}

// primaryLibraryName returns the platform-specific primary llama.cpp library
// filename used as the quick-verification anchor (see VerifyQuickForPlatform).
func primaryLibraryName(goos string) string {
	switch goos {
	case "darwin":
		return "libllama.dylib"
	case "linux":
		return "libllama.so"
	case "windows":
		return "llama.dll"
	default:
		return "libllama.so.0" // fallback
	}
}

// IsSecureOwnership checks Unix file permissions to ensure the file/directory
// is not group- or world-writable. On Windows, this always returns true.
// This is required for managed tiers 3-4.
func (pc *PlatformConfig) IsSecureOwnership(path string) (bool, error) {
	if pc.GOOS == "windows" {
		return true, nil // Windows permission model differs; skip this check
	}

	info, err := pc.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	mode := info.Mode()
	if mode&0022 != 0 { // group-writable or world-writable
		return false, nil
	}
	return isCurrentUserOwner(info)
}
