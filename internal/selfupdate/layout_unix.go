//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// systemPrefix is /usr/local: llmtui installs to /usr/local/bin/llmtui with
// its runtime at /usr/local/lib/llmtui/runtime, which the runtime resolver
// finds via <exe>/../lib/llmtui/runtime. OS-owned trees (/usr/bin, /bin,
// /System) are never touched.
func systemPrefix() (string, error) {
	return "/usr/local", nil
}

// userPrefix is ~/.local: ~/.local/bin/llmtui with ~/.local/lib/llmtui/runtime.
func userPrefix() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local"), nil
}

func layoutForPrefix(prefix string, scope Scope) Target {
	libDir := filepath.Join(prefix, "lib", "llmtui")
	return Target{
		Scope:        scope,
		Prefix:       prefix,
		BinPath:      filepath.Join(prefix, "bin", "llmtui"),
		RuntimeDir:   filepath.Join(libDir, "runtime"),
		DocDir:       filepath.Join(prefix, "share", "doc", "llmtui"),
		ManifestPath: filepath.Join(libDir, "install.json"),
	}
}
