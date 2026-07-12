//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
	"time"
)

// setupProcAttr puts the subprocess in its own process group so
// terminateProcess can signal the whole group (the command itself plus any
// wrapper grandchildren it spawns, e.g. npx/uvx/sh -c) instead of just the
// immediate child.
func setupProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess asks the process group to exit (SIGTERM), gives it a
// grace period to do so, then forces it (SIGKILL). Using -pid targets the
// whole group created by setupProcAttr, so grandchildren spawned by wrapper
// commands are reaped too, not just the direct child.
func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	_ = syscall.Kill(-pid, syscall.SIGTERM)

	select {
	case <-done:
		// The direct child is reaped, but a grandchild that ignores SIGTERM
		// (with its wrapper already gone) would survive: sweep the group.
		// ESRCH when the group is already empty is harmless.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return
	case <-time.After(2 * time.Second):
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}
