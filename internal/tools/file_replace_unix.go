//go:build !windows

package tools

import (
	"fmt"
	"os"
	"syscall"
)

// syncParentDir best-effort fsyncs the directory containing a just-published
// rename so the directory-entry update itself is durable across a crash, not
// just the file's data (POSIX fsync on a file descriptor does not guarantee
// its containing directory entry is durable — the directory has to be
// fsync'd separately). dirRel is "." for a file directly under the
// workspace root. A non-nil return is always reported by the caller as a
// durability-only warning, never as a write failure: by the time this runs,
// (*os.Root).Rename has already committed the publish.
func syncParentDir(root *os.Root, dirRel string) error {
	dir, err := root.Open(dirRel)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// checkNotHardLinked rejects publishing over a target that has more than one
// hard link, using the Nlink count Unix stat(2) metadata already exposes
// (no extra syscall). Renaming a staged file over one name of a
// multiply-linked file updates only that one directory entry — every other
// name still resolves to the old inode's old content, silently
// desynchronized from what this write just published. os.Root confines by
// path, not by inode, and enforces no exclusivity here; this check is the
// one hard-link defense this package can offer, and it only fires when the
// target already exists (a create has nothing to check).
func checkNotHardLinked(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return nil // platform stat metadata unavailable; nothing to check
	}
	if st.Nlink > 1 {
		return fmt.Errorf("has %d hard links; publishing here would only update this one name and leave the others pointing at the old content", st.Nlink)
	}
	return nil
}
