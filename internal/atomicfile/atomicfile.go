// Package atomicfile is the one way llmtui replaces a file on disk. A reader
// sees either the old content or the new content, never a truncated mix, and
// the new content is durable before Write returns.
//
// Every write goes through a fresh, uniquely named hidden temp file in the
// target's directory. Its mode is set with Chmod on the open file, so neither
// the process umask nor a stale leftover temp file can loosen it. The data is
// synced and closed, then the temp file replaces the target: rename plus a
// parent-directory fsync on unix, MoveFileEx with write-through on Windows.
// The temp file is removed on every error path.
//
// The mode stays a caller decision (most state is 0o600, the self-update
// install manifest is deliberately 0o644); naming, fsync and replace
// semantics are not.
//
// This is a leaf package: it imports only the standard library and
// golang.org/x/sys/windows. Workspace writes in internal/tools do not use it —
// they need os.Root confinement and hard-link checks this package does not
// provide.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrReplace marks a failure to move the finished temp file over the target.
// Everything before it succeeded, so a caller that tolerates a concurrent
// writer (for example an immutable cache entry) can check whether a complete
// target already exists.
var ErrReplace = errors.New("replace file")

// Write replaces path with whatever fill writes, using mode perm. fill must
// not retain the writer. An error from fill is returned unchanged.
func Write(path string, perm fs.FileMode, fill func(io.Writer) error) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if err := fill(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	return replaceFile(tmpPath, path)
}

// WriteFile replaces path with data, using mode perm.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return Write(path, perm, func(w io.Writer) error {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("write temporary file: %w", err)
		}
		return nil
	})
}
