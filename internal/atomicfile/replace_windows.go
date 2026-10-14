//go:build windows

package atomicfile

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// replaceFile moves from over to. MOVEFILE_WRITE_THROUGH returns only after
// the move is flushed to disk, which stands in for the unix directory fsync.
func replaceFile(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReplace, err)
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReplace, err)
	}
	if err := windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("%w: %w", ErrReplace, &os.LinkError{Op: "rename", Old: from, New: to, Err: err})
	}
	return nil
}
