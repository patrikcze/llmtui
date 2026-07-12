//go:build windows

package mcp

import "os/exec"

// setupProcAttr is a no-op on Windows for now. Job Objects would let us
// reap wrapper grandchildren the way process groups do on Unix; that is
// future work (see terminateProcess).
func setupProcAttr(cmd *exec.Cmd) {}

// terminateProcess kills the direct child process and waits for it to exit.
// This does not reap grandchildren spawned by wrapper commands (npx/uvx/etc);
// doing so on Windows requires assigning the process to a Job Object at
// creation time, which is left as future work.
func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
