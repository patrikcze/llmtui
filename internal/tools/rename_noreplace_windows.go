//go:build windows

package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// renameNoReplace renames src to dst (both root-relative, already confined
// and checked for symlink components) and fails with fs.ErrExist instead of
// replacing an existing dst. MoveFileEx without MOVEFILE_REPLACE_EXISTING
// refuses an existing destination atomically. It takes full paths, so unlike
// the Unix variants it cannot pin the parent directories through *os.Root:
// a parent swapped for a junction between the confinement check and this
// call is the documented residual.
func renameNoReplace(_ *os.Root, rootPath, src, dst string) error {
	from, err := windows.UTF16PtrFromString(filepath.Join(rootPath, src))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(rootPath, dst))
	if err != nil {
		return err
	}
	err = windows.MoveFileEx(from, to, 0)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_ALREADY_EXISTS), errors.Is(err, windows.ERROR_FILE_EXISTS):
		return fmt.Errorf("rename: %w", fs.ErrExist)
	default:
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
}
