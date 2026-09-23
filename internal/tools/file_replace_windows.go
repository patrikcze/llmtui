//go:build windows

package tools

import "os"

// syncParentDir is a documented no-op on Windows. NTFS/ReFS expose no
// directory-fsync primitive equivalent to POSIX fsync(dirfd), and
// (*os.Root).Open on a directory does not yield a handle Sync can usefully
// flush metadata through. The durability-relevant operation on this
// platform is (*os.Root).Rename itself, which uses
// FILE_RENAME_POSIX_SEMANTICS with REPLACE_IF_EXISTS (falling back to the
// classic ReplaceIfExists rename on filesystems without POSIX rename
// support, such as FAT) — see internal/syscall/windows.Renameat in the Go
// standard library (verified against the go1.27 source; not executed here,
// this environment is macOS — see phase-1b-report.md).
func syncParentDir(root *os.Root, dirRel string) error {
	return nil
}

// checkNotHardLinked is a documented no-op on Windows. NTFS hard links
// exist, but their link count is not exposed on the os.FileInfo Root.Stat
// already returns (unlike Unix's syscall.Stat_t.Nlink) — reading it would
// need a second handle-based syscall (GetFileInformationByHandle) this
// phase does not add. os.Root enforces no inode/file-ID exclusivity on any
// platform; a hard-linked write target on Windows is a documented residual,
// not a gap this function claims to close.
func checkNotHardLinked(info os.FileInfo) error {
	return nil
}
