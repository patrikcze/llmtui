//go:build windows

package procutil

import "os/exec"

// SetupProcAttr is a no-op until Windows Job Objects are supported.
func SetupProcAttr(cmd *exec.Cmd) {}

// Terminate kills and reaps the direct child. Reaping grandchildren on
// Windows requires assigning the process to a Job Object at creation time.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// KillGroup is a no-op until Windows Job Objects are supported.
func KillGroup(cmd *exec.Cmd) {}
