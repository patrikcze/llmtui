//go:build !windows

package tools

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// run_command is synchronous by contract (one command per block), so a
// backgrounded process must not outlive the tool call: the process group is
// killed once the command returns — on success as well as on timeout.
func TestRunCommandKillsBackgroundDescendants(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	out, err := r.runCommand("sleep 30 >/dev/null 2>&1 & echo $!")
	if err != nil {
		t.Fatalf("run: %v (output %q)", err, out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || pid <= 1 {
		t.Fatalf("expected the background pid in output, got %q", out)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // background process was reaped with the group
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("background process pid %d outlived run_command", pid)
}

func TestRunCommandTimeoutReapsGrandchild(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.CommandTimeout = 100 * time.Millisecond
	out, err := r.runCommand(`sleep 30 & echo $!; wait`)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(strings.Split(out, "\n")[0]))
	if parseErr != nil {
		t.Fatalf("no grandchild pid in output %q: %v", out, parseErr)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived the run_command timeout", pid)
}
