//go:build windows

package tools

import (
	"os"
	"syscall"
	"time"
)

// fileTimes reports a file's creation (birth) and status-change times when
// the platform's stat metadata exposes them; a zero time means unknown.
// Windows has no POSIX status-change time, so changed is always unknown.
func fileTimes(info os.FileInfo) (created, changed time.Time) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || data == nil {
		return time.Time{}, time.Time{}
	}
	return time.Unix(0, data.CreationTime.Nanoseconds()), time.Time{}
}
