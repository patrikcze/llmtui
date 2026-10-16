//go:build darwin

package tools

import (
	"os"
	"syscall"
	"time"
)

// fileTimes reports a file's creation (birth) and status-change times when
// the platform's stat metadata exposes them; a zero time means unknown.
func fileTimes(info os.FileInfo) (created, changed time.Time) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return time.Time{}, time.Time{}
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec)
}
