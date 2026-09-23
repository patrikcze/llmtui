//go:build windows

package entity

import "os"

// Windows does not expose a portable signal-zero operation through os.Process.
// A marker with a live process is conservatively retained; cleanup remains
// available after the marker is removed by the owner or explicit user action.
func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
