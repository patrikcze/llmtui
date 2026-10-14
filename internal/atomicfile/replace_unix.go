//go:build !windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceFile renames from over to, then syncs the parent directory so the
// new directory entry survives a crash.
func replaceFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("%w: %w", ErrReplace, err)
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
