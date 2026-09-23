package tools

import (
	"errors"
	"testing"
)

// formatResultFixtures is the representative fixture set FormatResults and
// NativeResults rendered before Phase 1a introduced Result.Meta and the
// shared formatResultContent helper. Every fixture below carries a non-empty
// Meta (proving Meta does not leak into rendered text this phase) except the
// first, which is the legacy zero-Meta shape any pre-Phase-1a caller still
// produces.
var formatResultFixtures = []Result{
	{
		Call:   Call{Tool: ToolWriteFile, Path: "a.sh"},
		Output: "wrote 10 bytes to a.sh",
		// Meta intentionally left zero-value here.
	},
	{
		Call: Call{Tool: ToolReadFile, Path: "missing"},
		Err:  errors.New("file not found"),
		Meta: ResultMeta{Outcome: OutcomeFailed, Effect: EffectNone, Error: &ErrorInfo{Code: "not_found", Retry: RetryCorrectInput, Message: "file not found"}},
	},
	{
		Call:   Call{Tool: ToolListDir, Path: "."},
		Output: "",
		Meta:   ResultMeta{Outcome: OutcomeOK, Effect: EffectNone, Coverage: Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true}},
	},
	{
		Call: Call{Tool: ToolRunCommand},
		Err:  errors.New("command failed: exit status 1"),
		// An error result can still carry partial Output (stdout/stderr
		// captured before the failure) — both must render.
		Output: "partial stdout before the failure",
		Meta:   ResultMeta{Outcome: OutcomeFailed, Effect: EffectUnknown, Coverage: Coverage{ObservedBytes: 34, RetainedBytes: 34}},
	},
	{
		Call:   Call{ID: "c1", Tool: ToolGrep, Path: "src"},
		Output: "src/main.go:1:package main",
		Meta:   ResultMeta{Outcome: OutcomePartial, Effect: EffectNone, Coverage: Coverage{SourceComplete: false, Reasons: []string{"files"}}},
	},
}

// TestFormatResultContentUnchanged proves the Phase 1a refactor into a
// shared formatResultContent helper renders byte-identical content to what
// FormatResults/NativeResults computed separately before this phase: the
// error line (if any) followed by output, with no Meta field appearing in
// the rendered text. Do not weaken this test to make a later phase's
// Meta-aware rendering pass — extend the fixture set instead.
func TestFormatResultContentUnchanged(t *testing.T) {
	want := []string{
		"wrote 10 bytes to a.sh",
		"error: file not found",
		"",
		"error: command failed: exit status 1\npartial stdout before the failure",
		"src/main.go:1:package main",
	}
	if len(want) != len(formatResultFixtures) {
		t.Fatalf("fixture/want length mismatch: %d vs %d", len(formatResultFixtures), len(want))
	}
	for i, res := range formatResultFixtures {
		if got := formatResultContent(res); got != want[i] {
			t.Errorf("formatResultContent(%d) = %q, want %q", i, got, want[i])
		}
	}
}

// TestFormatResultsSnapshot pins FormatResults' exact rendered output across
// the fixture set, including the "### tool path" headers and prefix, so a
// change to formatResultContent's Meta-blindness this phase — or a header
// change in a later phase — trips a visible diff here.
func TestFormatResultsSnapshot(t *testing.T) {
	want := ResultsPrefix + "\n" +
		"\n### write_file a.sh\n" +
		"wrote 10 bytes to a.sh\n" +
		"\n### read_file missing\n" +
		"error: file not found\n" +
		"\n### list_dir .\n" +
		"\n" +
		"\n### run_command\n" +
		"error: command failed: exit status 1\npartial stdout before the failure\n" +
		"\n### grep src\n" +
		"src/main.go:1:package main"
	if got := FormatResults(formatResultFixtures); got != want {
		t.Errorf("FormatResults snapshot changed:\ngot:  %q\nwant: %q", got, want)
	}
}

// TestNativeResultsSnapshot is NativeResults' equivalent pin: one
// role:"tool" message per call, Content built the same way
// FormatResults' body is, Meta never appearing in Content/ToolName/Display.
func TestNativeResultsSnapshot(t *testing.T) {
	msgs := NativeResults(formatResultFixtures)
	if len(msgs) != len(formatResultFixtures) {
		t.Fatalf("messages = %d, want %d", len(msgs), len(formatResultFixtures))
	}
	wantContent := []string{
		"wrote 10 bytes to a.sh",
		"error: file not found",
		"",
		"error: command failed: exit status 1\npartial stdout before the failure",
		"src/main.go:1:package main",
	}
	for i, msg := range msgs {
		if msg.Content != wantContent[i] {
			t.Errorf("msg[%d].Content = %q, want %q", i, msg.Content, wantContent[i])
		}
		if msg.ToolName != formatResultFixtures[i].Call.Tool {
			t.Errorf("msg[%d].ToolName = %q, want %q", i, msg.ToolName, formatResultFixtures[i].Call.Tool)
		}
		if msg.ToolCallID != formatResultFixtures[i].Call.ID {
			t.Errorf("msg[%d].ToolCallID = %q, want %q", i, msg.ToolCallID, formatResultFixtures[i].Call.ID)
		}
	}
}

// TestErrorCodeVocabularyIsClosedAndStable pins the exact §23 error-code set
// this phase may produce. Adding, removing, or
// renaming a code here is a deliberate contract change a caller can switch
// on, not a typo fix; this test exists so that change is visible in review.
func TestErrorCodeVocabularyIsClosedAndStable(t *testing.T) {
	want := []string{
		"invalid_arguments", "invalid_pattern", "not_found", "range_after_eof",
		"unsupported_content", "encoding_loss", "ambiguous_match",
		"match_not_found", "no_change", "network", "dns", "http_status",
		"unsupported_content_type", "cancelled", "timeout", "permission_denied",
		"safety_block", "repeat_block", "budget_block", "outcome_unknown",
		"symlink_write_unsupported", "resource_unavailable", "capture_limit",
		"source_changed", "resource_expired", "stale_source", "cursor_expired",
		"retention_unavailable", "wrong_resource_kind", "snapshot_incomplete", "cache_miss",
	}
	if len(errorCodeVocabulary) != len(want) {
		t.Fatalf("errorCodeVocabulary has %d codes, want %d", len(errorCodeVocabulary), len(want))
	}
	for _, code := range want {
		if !errorCodeVocabulary[code] {
			t.Errorf("errorCodeVocabulary missing %q", code)
		}
	}
}
