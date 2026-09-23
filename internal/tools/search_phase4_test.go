package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
)

func TestPhase4SearchDecodersAndLegacyBody(t *testing.T) {
	calls := Parse("```tool grep src\n{" + `"pattern":"Needle","literal":true,"case_sensitive":false,"context":2,"limit":7}` + "\n```\n")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Body != "Needle" || call.Path != "src" || !call.SearchLiteral || call.SearchCaseSensitive == nil || *call.SearchCaseSensitive || call.SearchContext != 2 || call.SearchLimit != 7 {
		t.Fatalf("decoded search call = %+v", call)
	}
	legacy := Parse("```tool grep\n{needle}\n```")
	if len(legacy) != 1 || strings.TrimSpace(legacy[0].Body) != "{needle}" {
		t.Fatalf("JSON-shaped legacy regex was not preserved: %+v", legacy)
	}
	native := CallsFromNative([]provider.ToolCall{{Name: ToolGrep, Arguments: `{"pattern":"Needle","literal":true,"limit":7}`}})
	if len(native) != 1 || native[0].Body != "Needle" || !native[0].SearchLiteral || native[0].SearchLimit != 7 {
		t.Fatalf("native search call = %+v", native)
	}
	bad := CallsFromNative([]provider.ToolCall{{Name: ToolGrep, Arguments: `{"pattern":"x","path":"a","resource_id":"ent_00001"}`}})
	if len(bad) != 1 || bad[0].InputErr == "" {
		t.Fatalf("path/resource conflict was accepted: %+v", bad)
	}
}

func TestPhase4SearchPagesAreStableAndCursorScoped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 6; i++ {
		writeSearchFixture(t, root, fmt.Sprintf("f%02d.txt", i), "needle\n")
	}
	r := NewRunner(root, 512)
	first := r.Execute(Call{Tool: ToolGrep, Body: "needle", SearchLimit: 2})
	if first.Err != nil || first.Meta.Window == nil {
		t.Fatalf("first page: err=%v meta=%+v output=%q", first.Err, first.Meta, first.Output)
	}
	if !strings.Contains(first.Output, "f00.txt") || strings.Contains(first.Output, "f02.txt") {
		t.Fatalf("unexpected first page: %q", first.Output)
	}
	if err := os.WriteFile(filepath.Join(root, "f02.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := r.Execute(Call{Tool: ToolGrep, SearchCursor: first.Meta.Window.NextCursor, SearchLimit: 2})
	if next.Err != nil {
		t.Fatalf("continuation: %v", next.Err)
	}
	if !strings.Contains(next.Output, "f02.txt") || !strings.Contains(next.Output, "f03.txt") {
		t.Fatalf("continuation reran the source instead of using captured rows: %q", next.Output)
	}
	other := r.Execute(Call{Tool: ToolGlob, Body: "*.txt", SearchLimit: 1})
	if other.Err != nil || other.Meta.Window == nil {
		t.Fatalf("glob page: err=%v meta=%+v", other.Err, other.Meta)
	}
	wrongKind := r.Execute(Call{Tool: ToolGrep, SearchCursor: other.Meta.Window.NextCursor})
	if wrongKind.Err == nil || errorInfoFor(wrongKind.Err).Code != "invalid_arguments" {
		t.Fatalf("cross-operation cursor was accepted: err=%v", wrongKind.Err)
	}
	listing := r.Execute(Call{Tool: ToolListDir, SearchLimit: 2})
	if listing.Err != nil || listing.Meta.Window == nil || !strings.Contains(listing.Output, "f00.txt") {
		t.Fatalf("list_dir page: err=%v meta=%+v output=%q", listing.Err, listing.Meta, listing.Output)
	}
	listingNext := r.Execute(Call{Tool: ToolListDir, SearchCursor: listing.Meta.Window.NextCursor, SearchLimit: 2})
	if listingNext.Err != nil || !strings.Contains(listingNext.Output, "f02.txt") {
		t.Fatalf("list_dir continuation: err=%v output=%q", listingNext.Err, listingNext.Output)
	}
	r.ResetSearchCursors()
	expired := r.Execute(Call{Tool: ToolGrep, SearchCursor: first.Meta.Window.NextCursor})
	if expired.Err == nil || errorInfoFor(expired.Err).Code != "cursor_expired" {
		t.Fatalf("reset cursor was not expired: err=%v", expired.Err)
	}
}

func TestPhase4SearchResourceAndCoverage(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 512)
	id, err := entity.ParseID("ent_00001")
	if err != nil {
		t.Fatal(err)
	}
	r.Resources = &fakeResourceReader{bodies: map[entity.ID][]byte{id: []byte("before\nNeedle here\nafter\n")}}
	res := r.Execute(Call{Tool: ToolGrep, Body: "needle", ResourceID: id.String(), SearchContext: 1, SearchCaseSensitive: func() *bool { v := false; return &v }()})
	if res.Err != nil || !strings.Contains(res.Output, "resource:"+id.String()) {
		t.Fatalf("resource search: err=%v output=%q", res.Err, res.Output)
	}
	if res.Meta.Search.MatchesReturned != 1 || res.Meta.Search.FilesScanned != 1 {
		t.Fatalf("resource coverage = %+v", res.Meta.Search)
	}
	r.Resources = &fakeResourceReader{err: entity.ErrNotAResourceBody}
	wrong := r.Execute(Call{Tool: ToolGrep, Body: "x", ResourceID: id.String()})
	if wrong.Err == nil || errorInfoFor(wrong.Err).Code != "wrong_resource_kind" {
		t.Fatalf("wrong resource kind: err=%v info=%+v", wrong.Err, errorInfoFor(wrong.Err))
	}
}

func TestPhase4SearchLongLineAndCancellation(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "long.txt", "needle"+strings.Repeat("x", maxSearchLineBytes)+"\n")
	r := NewRunner(root, 512)
	res := r.Execute(Call{Tool: ToolGrep, Body: "needle"})
	if res.Err != nil || res.Meta.Outcome != OutcomePartial || res.Meta.Search.TextComplete {
		t.Fatalf("long-line coverage = err=%v outcome=%s search=%+v", res.Err, res.Meta.Outcome, res.Meta.Search)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res = r.ExecuteContext(ctx, Call{Tool: ToolGrep, Body: "needle"})
	if res.Err == nil || !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("cancelled search err=%v", res.Err)
	}
}
