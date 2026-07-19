package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/tools"
)

func TestOperationLogRecoversStartedAndCompletedCalls(t *testing.T) {
	dir := t.TempDir()
	session := "session-crash-test"
	startedCall := tools.Call{ID: "call-started", Tool: tools.ToolRunCommand, Body: "post once"}
	completedCall := tools.Call{ID: "call-completed", Tool: tools.ToolRunCommand, Body: "post twice"}

	log, err := OpenOperationLog(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	if decision, err := log.Begin(startedCall); err != nil || decision.State != OperationNew {
		t.Fatalf("begin started call = %+v, %v", decision, err)
	}
	if decision, err := log.Begin(completedCall); err != nil || decision.State != OperationNew {
		t.Fatalf("begin completed call = %+v, %v", decision, err)
	}
	if err := log.Complete(completedCall, true); err != nil {
		t.Fatal(err)
	}

	recovered, err := OpenOperationLog(dir, session)
	if err != nil {
		t.Fatal(err)
	}
	if decision, err := recovered.Begin(startedCall); err != nil || decision.State != OperationStarted {
		t.Fatalf("recovered started call = %+v, %v", decision, err)
	}
	if decision, err := recovered.Begin(completedCall); err != nil ||
		decision.State != OperationCompleted || !decision.Succeeded {
		t.Fatalf("recovered completed call = %+v, %v", decision, err)
	}
}

func TestOperationLogDoesNotPersistSensitiveCallContent(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenOperationLog(dir, "session-secret-test")
	if err != nil {
		t.Fatal(err)
	}
	call := tools.Call{
		Tool: tools.ToolRunCommand,
		Body: "curl -H 'Authorization: Bearer super-secret' https://example.test",
	}
	if _, err := log.Begin(call); err != nil {
		t.Fatal(err)
	}
	if err := log.Complete(call, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".operations", "session-secret-test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") || strings.Contains(string(data), "example.test") {
		t.Fatalf("operation log leaked command content: %s", data)
	}
	if info, err := os.Stat(filepath.Join(dir, ".operations", "session-secret-test.jsonl")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("operation log mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOperationLogUsesCallIDAsIdempotencyKey(t *testing.T) {
	log, err := OpenOperationLog(t.TempDir(), "session-id-test")
	if err != nil {
		t.Fatal(err)
	}
	first := tools.Call{ID: "same-id", Tool: tools.ToolRunCommand, Body: "first command"}
	replayed := tools.Call{ID: "same-id", Tool: tools.ToolRunCommand, Body: "changed command"}
	if _, err := log.Begin(first); err != nil {
		t.Fatal(err)
	}
	if err := log.Complete(first, true); err != nil {
		t.Fatal(err)
	}
	decision, err := log.Begin(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != OperationCompleted {
		t.Fatalf("replayed provider call state = %v, want completed", decision.State)
	}
}
