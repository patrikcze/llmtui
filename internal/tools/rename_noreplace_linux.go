//go:build linux

package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames src to dst (both root-relative, already confined
// and checked for symlink components) and fails with fs.ErrExist instead of
// replacing an existing dst. The parent directories are opened through
// *os.Root and renameat2(RENAME_NOREPLACE) gets single-component names, so
// the kernel performs the existence check and the rename atomically.
// Filesystems without RENAME_NOREPLACE fall back to renameCheckedFallback.
func renameNoReplace(root *os.Root, _ string, src, dst string) error {
	srcDir, err := root.Open(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer srcDir.Close()
	dstDir, err := root.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dstDir.Close()
	err = unix.Renameat2(int(srcDir.Fd()), filepath.Base(src), int(dstDir.Fd()), filepath.Base(dst), unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("rename: %w", fs.ErrExist)
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		return renameCheckedFallback(root, src, dst)
	default:
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
}
