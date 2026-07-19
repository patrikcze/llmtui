//go:build windows

package procutil

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTrackProcessAssignsJobObject(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "ping -n 30 127.0.0.1 >NUL")
	SetupProcAttr(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("SetupProcAttr did not create a process group")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	windowsJobs.Lock()
	job := windowsJobs.byPID[cmd.Process.Pid]
	windowsJobs.Unlock()
	if job == 0 {
		t.Fatal("started process has no tracked Job Object")
	}
	Terminate(cmd)
	windowsJobs.Lock()
	_, stillTracked := windowsJobs.byPID[cmd.Process.Pid]
	windowsJobs.Unlock()
	if stillTracked {
		t.Fatal("Terminate did not release the Job Object")
	}
}
