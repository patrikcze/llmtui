//go:build !windows

package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// renameCheckedFallback is the no-replace rename for filesystems or
// platforms without an atomic primitive: an existence check followed by
// (*os.Root).Rename. llmtui's own writes are serialized by the execution
// gate, so only an external process creating dst in the gap between the
// check and the rename could be overwritten — the documented residual.
func renameCheckedFallback(root *os.Root, src, dst string) error {
	if _, err := root.Lstat(dst); err == nil {
		return fmt.Errorf("rename: %w", fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return root.Rename(src, dst)
}
