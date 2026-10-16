//go:build !darwin && !linux && !windows

package tools

import (
	"os"
	"time"
)

// fileTimes reports a file's creation and status-change times when the
// platform exposes them; this platform is not wired, so both are unknown.
func fileTimes(os.FileInfo) (created, changed time.Time) {
	return time.Time{}, time.Time{}
}
