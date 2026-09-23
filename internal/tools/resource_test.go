package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
)

// fakeResourceReader is a minimal in-memory tools.ResourceReader stand-in for
// *entity.Registry, so this package's tests do not need to import
// internal/entity's Registry construction just to exercise read_file's
// resource_id path.
type fakeResourceReader struct {
	bodies map[entity.ID][]byte
	err    error // when set, OpenBody always fails with this error
}

func (f *fakeResourceReader) OpenBody(_ context.Context, id entity.ID) (entity.ResourceView, entity.BodyLease, error) {
	if f.err != nil {
		return entity.ResourceView{}, nil, f.err
	}
	data, ok := f.bodies[id]
	if !ok {
		return entity.ResourceView{}, nil, fmt.Errorf("resource %q not present", id)
	}
	return entity.ResourceView{ID: id, SizeBytes: int64(len(data))}, &fakeBodyLease{data: data}, nil
}

type fakeBodyLease struct {
	data   []byte
	closed bool
}

func (l *fakeBodyLease) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(l.data).ReadAt(p, off)
}
func (l *fakeBodyLease) Size() int64  { return int64(len(l.data)) }
func (l *fakeBodyLease) Close() error { l.closed = true; return nil }

// TestRunCommandCappedOutputProducesCapture proves run_command builds a
// Capture (Phase 2b-ii) exactly when its preview was capped — mirroring
// TestRunCommandOutputCapturedBoundedByRetentionCap's Phase 2a scenario, but
// asserting on Result.Captures rather than only Meta.Coverage.
func TestRunCommandCappedOutputProducesCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	root := t.TempDir()
	r := NewRunner(root, 8) // 8 KB retention cap
	const produced = 1 << 20

	res := r.Execute(Call{Tool: ToolRunCommand, Body: fmt.Sprintf("yes | head -c %d", produced)})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if len(res.Captures) != 1 {
		t.Fatalf("Captures = %d, want 1 for a capped result", len(res.Captures))
	}
	built := res.Captures[0]
	if built.Kind != entity.KindToolOutput {
		t.Errorf("Capture.Kind = %q, want %q", built.Kind, entity.KindToolOutput)
	}
	if built.Trust != entity.TrustWorkspaceUntrusted {
		t.Errorf("Capture.Trust = %q, want %q", built.Trust, entity.TrustWorkspaceUntrusted)
	}
	if len(built.Body) != 8*1024 {
		t.Errorf("Capture.Body = %d bytes, want the full 8 KB retention cap", len(built.Body))
	}
	if built.BodyDigest == "" || built.BodyDigest != res.Meta.ContentDigest {
		t.Errorf("Capture.BodyDigest = %q, want it to equal Meta.ContentDigest %q (reused, not rehashed)", built.BodyDigest, res.Meta.ContentDigest)
	}
	// The retained capture is exactly the bytes already visible in the capped
	// Output preview (mod the trailing truncation marker/newline trim) — see
	// resource.go's doc comment: this phase's retention cap and its own
	// display cap are the same number, so a resource_id read-back recovers
	// what was already shown, not additional bytes beyond it.
	if !strings.Contains(res.Output, "truncated") {
		t.Fatalf("expected a truncated preview, got %q", res.Output)
	}
}

// TestRunCommandUncappedOutputHasNoCapture proves the common case — output
// well under the retention cap — produces no Capture at all, matching
// pre-Phase-2b-ii behavior exactly (nothing extra to retain beyond Output).
func TestRunCommandUncappedOutputHasNoCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	root := t.TempDir()
	r := NewRunner(root, 64)
	res := r.Execute(Call{Tool: ToolRunCommand, Body: "echo hello"})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if len(res.Captures) != 0 {
		t.Fatalf("Captures = %d, want 0 for an uncapped result", len(res.Captures))
	}
}

// TestReadFileResourceIDRecoversPublishedBody proves the consumer half of
// the mechanism: given a wired ResourceReader with a body already published
// under an ID, read_file(resource_id=...) returns exactly those bytes.
func TestReadFileResourceIDRecoversPublishedBody(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	body := []byte("the full retained command output, byte for byte")
	id := entity.ID("ent_" + strings.Repeat("a", 26))
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{id: body}}

	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: string(id)})
	if res.Err != nil {
		t.Fatalf("read_file resource_id: %v", res.Err)
	}
	if res.Output != string(body) {
		t.Fatalf("Output = %q, want the full retained body %q", res.Output, body)
	}
	if res.Meta.Outcome != OutcomeOK {
		t.Errorf("Outcome = %q, want OutcomeOK", res.Meta.Outcome)
	}
	if !res.Meta.Coverage.SourceComplete || !res.Meta.Coverage.CaptureComplete || !res.Meta.Coverage.PreviewComplete {
		t.Errorf("Coverage = %+v, want a fully-complete read under the display cap", res.Meta.Coverage)
	}
}

// TestReadFileResourceIDLargerThanCapIsMarkedIncomplete proves a retained
// body bigger than the runner's own maxKB display cap is truncated on
// read-back, with Coverage.SourceComplete=false distinguishing this
// display-cap truncation from the original command's own capture-time cap
// (a separate, already-reported fact) — see resource.go's readResourceMeta.
func TestReadFileResourceIDLargerThanCapIsMarkedIncomplete(t *testing.T) {
	r := NewRunner(t.TempDir(), 1) // 1 KB display cap
	body := bytes.Repeat([]byte("x"), 4096)
	id := entity.ID("ent_" + strings.Repeat("b", 26))
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{id: body}}

	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: string(id)})
	if res.Err != nil {
		t.Fatalf("read_file resource_id: %v", res.Err)
	}
	if len(res.Output) > 1024+64 {
		t.Fatalf("Output = %d bytes, want it bounded near the 1 KB display cap", len(res.Output))
	}
	if res.Meta.Coverage.SourceComplete {
		t.Error("SourceComplete = true, want false: the retained body exceeds the display cap")
	}
	if res.Meta.Outcome != OutcomePartial {
		t.Errorf("Outcome = %q, want OutcomePartial", res.Meta.Outcome)
	}
}

// TestReadFileResourceIDWithoutWiringFailsCleanly proves r.Resources == nil
// (entities disabled, or a runner that never had one wired) fails with a
// stable, closed error code — never a panic, never silently falling back to
// a path-based read.
func TestReadFileResourceIDWithoutWiringFailsCleanly(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: "ent_" + strings.Repeat("c", 26)})
	if res.Err == nil {
		t.Fatal("expected an error when no ResourceReader is wired")
	}
	if res.Meta.Error == nil || res.Meta.Error.Code != "unsupported_content" {
		t.Fatalf("Meta.Error = %+v, want code unsupported_content", res.Meta.Error)
	}
}

// TestReadFileResourceIDInvalidShapeIsInvalidArguments proves a malformed
// resource_id (not entity.ParseID's closed format) is rejected before ever
// reaching the ResourceReader.
func TestReadFileResourceIDInvalidShapeIsInvalidArguments(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{}}
	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: "not-a-real-id"})
	if res.Err == nil {
		t.Fatal("expected an error for a malformed resource_id")
	}
	if res.Meta.Error == nil || res.Meta.Error.Code != "invalid_arguments" {
		t.Fatalf("Meta.Error = %+v, want code invalid_arguments", res.Meta.Error)
	}
}

// TestReadFileResourceIDNotFoundIsResourceUnavailable proves any OpenBody
// failure (not present, expired, wrong kind) maps to the single
// resource_unavailable code this phase uses — see readResourceMeta's doc
// comment for why finer §23 granularity is deferred.
func TestReadFileResourceIDNotFoundIsResourceUnavailable(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.Resources = &fakeResourceReader{err: errors.New("gone")}
	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: "ent_" + strings.Repeat("d", 26)})
	if res.Err == nil {
		t.Fatal("expected an error for a not-present resource")
	}
	if res.Meta.Error == nil || res.Meta.Error.Code != "resource_unavailable" {
		t.Fatalf("Meta.Error = %+v, want code resource_unavailable", res.Meta.Error)
	}
}

func TestReadFileResourceIDExpiredIsDistinct(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.Resources = &fakeResourceReader{err: errors.New("entity body lifetime has ended")}
	res := r.Execute(Call{Tool: ToolReadFile, ResourceID: "ent_" + strings.Repeat("f", 26)})
	if res.Err == nil || res.Meta.Error == nil || res.Meta.Error.Code != "resource_expired" {
		t.Fatalf("result = %+v, want resource_expired", res)
	}
}

// TestReadFilePathAndResourceIDTogetherIsInvalidArguments proves the
// exactly-one-selector rule: both set is rejected before either is read,
// never silently preferring one over the other.
func TestReadFilePathAndResourceIDTogetherIsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 64)
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{}}
	res := r.Execute(Call{Tool: ToolReadFile, Path: "README.md", ResourceID: "ent_" + strings.Repeat("e", 26)})
	if res.Err == nil {
		t.Fatal("expected an error when both path and resource_id are set")
	}
	if res.Meta.Error == nil || res.Meta.Error.Code != "invalid_arguments" {
		t.Fatalf("Meta.Error = %+v, want code invalid_arguments", res.Meta.Error)
	}
}

// TestNativeReadFileDecodesResourceID proves CallsFromNative extracts
// resource_id from a native tool call's JSON arguments, alongside the
// existing offset/limit extraction.
func TestNativeReadFileDecodesResourceID(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{
		{ID: "1", Name: ToolReadFile, Arguments: `{"resource_id":"ent_abc"}`},
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].ResourceID != "ent_abc" {
		t.Errorf("ResourceID = %q, want ent_abc", calls[0].ResourceID)
	}
	if calls[0].InputErr != "" {
		t.Errorf("InputErr = %q, want empty", calls[0].InputErr)
	}
}

// TestFencedReadFileDecodesResourceID proves the fenced-block JSON body path
// (decodeReadFileBody) extracts resource_id the same way the native path
// does.
func TestFencedReadFileDecodesResourceID(t *testing.T) {
	reply := "```tool read_file\n{\"resource_id\":\"ent_xyz\"}\n```"
	calls := Parse(reply)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].ResourceID != "ent_xyz" {
		t.Errorf("ResourceID = %q, want ent_xyz", calls[0].ResourceID)
	}
}

// BenchmarkReadFileResourceID measures the read_file resource_id call path
// (ExecuteContext -> readResourceMeta -> ResourceReader.OpenBody -> lease
// read) at a realistic 64 KiB body, mirroring internal/entity's
// BenchmarkOpenBody one layer up the stack. Only runs with `go test -bench`.
func BenchmarkReadFileResourceID(b *testing.B) {
	r := NewRunner(b.TempDir(), 64)
	body := bytes.Repeat([]byte("x"), 64*1024)
	id := entity.ID("ent_" + strings.Repeat("f", 26))
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{id: body}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolReadFile, ResourceID: string(id)})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}
