//go:build linux

package tools

import (
	"os"
	"syscall"
	"time"
)

// fileTimes reports a file's creation (birth) and status-change times when
// the platform's stat metadata exposes them; a zero time means unknown.
// Linux stat(2) carries no birth time, so created is always unknown here.
func fileTimes(info os.FileInfo) (created, changed time.Time) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return time.Time{}, time.Time{}
	}
	return time.Time{}, time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)) //nolint:unconvert // field widths differ across GOARCH
}
