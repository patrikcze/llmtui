//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// systemPrefix is %ProgramFiles%\llmtui. The binary lives directly in the
// prefix (llmtui.exe) with its runtime at lib\llmtui\runtime, which the
// runtime resolver finds via <exe>\lib\llmtui\runtime.
func systemPrefix() (string, error) {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		return "", fmt.Errorf("%%ProgramFiles%% is not set")
	}
	return filepath.Join(pf, "llmtui"), nil
}

// userPrefix is %LOCALAPPDATA%\Programs\llmtui.
func userPrefix() (string, error) {
	lad := os.Getenv("LOCALAPPDATA")
	if lad == "" {
		return "", fmt.Errorf("%%LOCALAPPDATA%% is not set")
	}
	return filepath.Join(lad, "Programs", "llmtui"), nil
}

func layoutForPrefix(prefix string, scope Scope) Target {
	libDir := filepath.Join(prefix, "lib", "llmtui")
	return Target{
		Scope:        scope,
		Prefix:       prefix,
		BinPath:      filepath.Join(prefix, "llmtui.exe"),
		RuntimeDir:   filepath.Join(libDir, "runtime"),
		DocDir:       filepath.Join(prefix, "doc"),
		ManifestPath: filepath.Join(libDir, "install.json"),
	}
}
