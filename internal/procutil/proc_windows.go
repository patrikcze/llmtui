//go:build windows

package procutil

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsJobs = struct {
	sync.Mutex
	byPID map[int]windows.Handle
}{byPID: make(map[int]windows.Handle)}

// SetupProcAttr gives console children their own process group. TrackProcess
// performs the stronger Job Object assignment immediately after Start.
func SetupProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &windows.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// TrackProcess assigns a started child to a Job Object configured with
// KILL_ON_JOB_CLOSE. Windows has no Unix-style process groups that reliably
// contain descendants; the Job Object is the OS primitive that does.
func TrackProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process has not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("configure job object: %w", err)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("open child process: %w", err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assign child to job object: %w", err)
	}

	windowsJobs.Lock()
	if old := windowsJobs.byPID[cmd.Process.Pid]; old != 0 {
		windows.CloseHandle(old)
	}
	windowsJobs.byPID[cmd.Process.Pid] = job
	windowsJobs.Unlock()
	return nil
}

// Terminate kills the complete Job Object and reaps the direct child.
func Terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if job := takeJob(cmd.Process.Pid); job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// KillGroup terminates every process in the tracked Job Object. The direct
// child has already been reaped by callers that use this function.
func KillGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if job := takeJob(cmd.Process.Pid); job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
}

func takeJob(pid int) windows.Handle {
	windowsJobs.Lock()
	defer windowsJobs.Unlock()
	job := windowsJobs.byPID[pid]
	delete(windowsJobs.byPID, pid)
	return job
}
