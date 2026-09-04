//go:build !windows

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

func isReadOnlyFS(err error) bool {
	return errors.Is(err, syscall.EROFS)
}

func elevatedHint() string {
	return "Re-run the same command from an elevated shell, e.g.:\n\n  sudo llmtui self install --system"
}

// binDirOnPath reports whether dir is present in $PATH (exact element match).
func binDirOnPath(dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p != "" && filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

// updateSystemPath is a no-op on Unix: llmtui never edits shell profiles or
// system PATH configuration. `self install` only prints guidance when the bin
// directory is not already on PATH.
func updateSystemPath(_ string, _ Scope) (changed bool, note string, err error) {
	return false, "", nil
}
