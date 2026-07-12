//go:build !windows

package procutil

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitUntilDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", pid, timeout)
}

func TestTerminateReapsGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(sh, "-c", "sleep 30 & echo $!; wait")
	SetupProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	Terminate(cmd)
	waitUntilDead(t, pid, 2*time.Second)
}

func TestTerminateReapsStubbornGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(sh, "-c", `sh -c 'trap "" TERM; echo ready; sleep 30' & echo $!; wait`)
	SetupProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	r := bufio.NewReader(stdout)
	pid := 0
	for range 2 {
		line, readErr := r.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read startup: %v", readErr)
		}
		if value, parseErr := strconv.Atoi(strings.TrimSpace(line)); parseErr == nil {
			pid = value
		}
	}
	if pid == 0 {
		t.Fatal("grandchild pid not reported")
	}
	Terminate(cmd)
	waitUntilDead(t, pid, 2*time.Second)
}
