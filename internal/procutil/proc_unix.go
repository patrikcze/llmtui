//go:build !windows

package procutil

import (
	"os/exec"
	"syscall"
	"time"
)

// SetupProcAttr puts the subprocess in its own process group.
func SetupProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// TrackProcess is a no-op on Unix: SetupProcAttr establishes containment
// before Start.
func TrackProcess(cmd *exec.Cmd) error { return nil }

// Terminate asks the process group to exit, gives it a grace period, then
// forces it and reaps the direct child.
//
// Signaling -pid after the leader is reaped has a known, accepted TOCTOU: the
// kernel could recycle the pgid for an unrelated group in that tiny window.
// Avoiding that requires pidfd/cgroup support; leaving descendants running is
// the more likely and more harmful outcome.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}

// KillGroup force-kills the process group without waiting. It is intended for
// callers whose os/exec operation already reaped the direct child.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
