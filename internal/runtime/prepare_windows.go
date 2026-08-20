//go:build windows

package runtime

import (
	"fmt"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

var preparedDLLDirectories sync.Map

// PrepareLibraryDir adds the absolute runtime directory to Windows DLL
// dependency lookup. The returned cookie remains live for the process lifetime.
func PrepareLibraryDir(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve absolute DLL directory: %w", err)
	}
	absDir = filepath.Clean(absDir)
	if _, ok := preparedDLLDirectories.Load(absDir); ok {
		return nil
	}
	utf16Dir, err := windows.UTF16PtrFromString(absDir)
	if err != nil {
		return fmt.Errorf("encode DLL directory: %w", err)
	}
	cookie, err := windows.AddDllDirectory(utf16Dir)
	if err != nil {
		return fmt.Errorf("add DLL directory %q: %w", absDir, err)
	}
	actual, loaded := preparedDLLDirectories.LoadOrStore(absDir, cookie)
	if loaded {
		_ = windows.RemoveDllDirectory(cookie)
		_ = actual
	}
	return nil
}
