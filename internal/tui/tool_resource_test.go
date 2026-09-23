package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/tools"
)

// resourceIDPattern matches a Registry.Publish-minted entity ID (ent_ plus
// 26 lowercase base32 characters) — see internal/entity/id_random.go.
var resourceIDPattern = regexp.MustCompile(`ent_[a-z2-7]{26}`)

// TestRunCommandCapturedOutputRecoverableViaReadFileResourceID is the
// Phase 2b-ii end-to-end proof: a run_command result whose preview was
// capped by Phase 2a's bounded writer carries a resource_id the model can
// recover the FULL retained bytes from via read_file, without rerunning the
// command — and a session reset makes that ID unresolvable again. This
// exercises the real TUI publishing path (m.registerResultEntities), which
// is where run_command's Captures actually get turned into a resource_id
// reference; internal/tools' own resource_test.go covers the producer/
// consumer mechanics in isolation.
func TestRunCommandCapturedOutputRecoverableViaReadFileResourceID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 8) // 8 KB display/retention cap
	m.toolRunner.Resources = newResourceAdapter(m.entities)
	m.cfg.Entities.Enabled = true
	// OutputStorage left at its zero value ("") deliberately: this proves
	// the empty-defaults-to-memory read-site fallback (EntitiesConfig.
	// OutputStorage's doc comment), not just an explicitly-set "memory".

	const produced = 1 << 16 // 64 KiB: many multiples of the 8 KB cap.
	// counter.txt records how many times the command actually ran, so "no
	// replay" is a concrete, observable side effect rather than an
	// assumption about the code path.
	cmd := fmt.Sprintf("printf ran >> counter.txt && yes | head -c %d", produced)
	counterPath := filepath.Join(root, "counter.txt")

	res := m.toolRunner.Execute(tools.Call{Tool: tools.ToolRunCommand, Body: cmd})
	if res.Err != nil {
		t.Fatalf("run_command: %v", res.Err)
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Fatalf("expected a capped preview, got %q", res.Output)
	}
	if len(res.Captures) != 1 {
		t.Fatalf("Captures = %d, want 1 for a capped result", len(res.Captures))
	}

	results := m.registerResultEntities([]tools.Result{res})
	id := resourceIDPattern.FindString(results[0].Output)
	if id == "" {
		t.Fatalf("capped run_command result carries no resource_id reference: %q", results[0].Output)
	}

	if data, err := os.ReadFile(counterPath); err != nil {
		t.Fatalf("read counter.txt: %v", err)
	} else if string(data) != "ran" {
		t.Fatalf("counter.txt = %q, want exactly one run's marker %q", data, "ran")
	}

	readRes := m.toolRunner.Execute(tools.Call{Tool: tools.ToolReadFile, ResourceID: id})
	if readRes.Err != nil {
		t.Fatalf("read_file resource_id=%s: %v", id, readRes.Err)
	}
	// The runner's retention cap equals its own display cap this phase (see
	// internal/tools/resource.go's doc comment), so the recovered body is
	// exactly the retained 8 KiB of 'y' bytes — not the capped preview's
	// "\n… output truncated" marker text, and not a re-execution of the
	// command (which would still only ever produce the same 'y' bytes, so
	// this assertion alone does not prove no-replay — the counter.txt check
	// below does).
	if len(readRes.Output) != 8*1024 {
		t.Fatalf("recovered resource body = %d bytes, want the full 8 KiB retention cap", len(readRes.Output))
	}
	if strings.Contains(readRes.Output, "truncated") {
		t.Fatal("recovered resource body must not carry the preview's truncation marker")
	}
	if strings.Trim(readRes.Output, "y\n") != "" {
		t.Fatalf("recovered resource body contains unexpected bytes: %q", readRes.Output)
	}

	// The command was never rerun to satisfy the read_file recovery above.
	if data, err := os.ReadFile(counterPath); err != nil {
		t.Fatalf("read counter.txt after recovery: %v", err)
	} else if string(data) != "ran" {
		t.Fatalf("counter.txt = %q after recovery, want it unchanged — the command must not have been replayed", data)
	}

	// A session reset makes the old ID unavailable (in-process reset case;
	// saved-session-format behavior is a later phase's scope).
	m.resetEntities()
	afterReset := m.toolRunner.Execute(tools.Call{Tool: tools.ToolReadFile, ResourceID: id})
	if afterReset.Err == nil {
		t.Fatal("expected the resource_id to be unresolvable after a session reset")
	}
	if afterReset.Meta.Error == nil || afterReset.Meta.Error.Code != "resource_unavailable" {
		t.Fatalf("Meta.Error = %+v after reset, want code resource_unavailable", afterReset.Meta.Error)
	}
}

// TestRunCommandOutputStorageOffSkipsResourcePublishing proves
// entities.output_storage=off leaves a capped run_command result exactly as
// it was before Phase 2b-ii (a capped preview, no resource_id reference,
// nothing published) and that a read_file(resource_id=...) call against a
// made-up ID still fails cleanly rather than panicking — confirming
// Runner.Resources stays wired-but-empty in this configuration (see
// rebuildFromConfig's unconditional wiring in app.go), not nil.
func TestRunCommandOutputStorageOffSkipsResourcePublishing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 8)
	m.toolRunner.Resources = newResourceAdapter(m.entities)
	m.cfg.Entities.Enabled = true
	m.cfg.Entities.OutputStorage = "off"

	res := m.toolRunner.Execute(tools.Call{Tool: tools.ToolRunCommand, Body: fmt.Sprintf("yes | head -c %d", 1<<16)})
	if res.Err != nil {
		t.Fatalf("run_command: %v", res.Err)
	}
	if len(res.Captures) == 0 {
		t.Fatal("the producer still builds a Capture regardless of output_storage — the TUI publish gate, not the producer, is what output_storage controls")
	}

	results := m.registerResultEntities([]tools.Result{res})
	if resourceIDPattern.MatchString(results[0].Output) {
		t.Fatalf("output_storage=off must not publish a resource or reference one: %q", results[0].Output)
	}

	// m.toolRunner.Resources is wired (non-nil) but the registry has nothing
	// published under this made-up ID: confirms the "wired-but-empty" case,
	// not a nil ResourceReader, and that it still fails cleanly either way.
	if m.toolRunner.Resources == nil {
		t.Fatal("expected Resources to stay wired even when output_storage is off")
	}
	fake := m.toolRunner.Execute(tools.Call{Tool: tools.ToolReadFile, ResourceID: "ent_" + strings.Repeat("z", 26)})
	if fake.Err == nil {
		t.Fatal("expected a made-up resource_id to fail cleanly, not resolve")
	}
	if fake.Meta.Error == nil || fake.Meta.Error.Code != "resource_unavailable" {
		t.Fatalf("Meta.Error = %+v, want code resource_unavailable", fake.Meta.Error)
	}
}

// TestAppendTerminalToolResultsPublishesCaptures closes a gap the plan's
// Phase 2b checklist called out explicitly ("finalizeToolResults once for
// normal/controller/terminal paths") that neither Phase 2b-ii nor any later
// phase actually closed: sendToolResults (the continuing-conversation path)
// has always called registerResultEntities first, but
// appendTerminalToolResults (the last batch of an agent run, or a budget/
// ask_user termination — see its three call sites in agent_loop.go, app.go,
// and ask_user.go) delivered results directly, skipping entity/capture
// registration entirely. A capped run_command result in the FINAL batch of
// an agent run therefore lost its resource_id forever — there is no later
// turn to ask for it. This predates Phase 2b (verified against the
// pre-plan baseline, commit b6c57a7, where appendTerminalToolResults never
// called any entity-registration step either) but only became an observable
// resource-loss bug once Phase 2b-ii gave captures something to publish.
func TestAppendTerminalToolResultsPublishesCaptures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 8) // 8 KB display/retention cap
	m.toolRunner.Resources = newResourceAdapter(m.entities)
	m.cfg.Entities.Enabled = true

	res := m.toolRunner.Execute(tools.Call{Tool: tools.ToolRunCommand, Body: fmt.Sprintf("yes | head -c %d", 1<<16)})
	if res.Err != nil {
		t.Fatalf("run_command: %v", res.Err)
	}
	if len(res.Captures) != 1 {
		t.Fatalf("Captures = %d, want 1 for a capped result", len(res.Captures))
	}

	before := len(m.session.Messages)
	m.appendTerminalToolResults([]tools.Result{res})
	if len(m.session.Messages) != before+1 {
		t.Fatalf("session gained %d messages, want exactly 1", len(m.session.Messages)-before)
	}
	appended := m.session.Messages[len(m.session.Messages)-1]
	id := resourceIDPattern.FindString(appended.Content)
	if id == "" {
		t.Fatalf("terminal-path delivery lost the capped result's resource_id reference: %q", appended.Content)
	}

	// The published body is genuinely recoverable through the same
	// read_file(resource_id=...) path the continuing-conversation case
	// proves above — not just a reference string with nothing behind it.
	readRes := m.toolRunner.Execute(tools.Call{Tool: tools.ToolReadFile, ResourceID: id})
	if readRes.Err != nil {
		t.Fatalf("read_file resource_id=%s: %v", id, readRes.Err)
	}
	if len(readRes.Output) != 8*1024 {
		t.Fatalf("recovered resource body = %d bytes, want the full 8 KiB retention cap", len(readRes.Output))
	}
}
