package memoryindex

import (
	"strings"
	"testing"
	"time"
)

func TestEstimateHitTokensCountsFraming(t *testing.T) {
	text := strings.Repeat("x", 400)
	hit := Hit{Item: Item{
		ID: "a.go#1-40", Kind: KindSourceChunk, Scope: ScopeProject, Text: text,
		Trust:     TrustWorkspaceUntrusted,
		Source:    SourceRef{Path: "internal/pkg/a.go", StartLine: 1, EndLine: 40},
		UpdatedAt: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	}}
	textOnly := (len(text) + 3) / 4
	got := estimateHitTokens(hit)
	if got < textOnly+50 {
		t.Errorf("estimate %d barely exceeds text-only %d; framing is not counted", got, textOnly)
	}
	if got > textOnly+150 {
		t.Errorf("estimate %d is implausibly far above text-only %d", got, textOnly)
	}
}

func TestEstimateHitTokensGrowsWithProvenance(t *testing.T) {
	short := Hit{Item: Item{ID: "a", Kind: KindSourceChunk, Text: "x", Source: SourceRef{Path: "a.go", StartLine: 1, EndLine: 1}}}
	long := short
	long.Item.ID = strings.Repeat("d", 200)
	long.Item.Source.Path = strings.Repeat("dir/", 50) + "a.go"
	if estimateHitTokens(long) <= estimateHitTokens(short) {
		t.Error("a long path and ID must cost more than a short one")
	}
}

func TestSourceLabelByKind(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
	}{
		{"user", Item{Kind: KindUserPreference}, "user"},
		{"project", Item{Kind: KindProjectDecision, ProjectID: "0123456789abcdef"}, "project:0123456789ab"},
		{"project run", Item{Kind: KindProjectDecision, ProjectID: "p", Source: SourceRef{RunID: "r1", Cycle: 2}}, "project:p/run:r1/cycle:2"},
		{"episode", Item{Kind: KindEpisode, SessionID: "s1"}, "session:s1"},
		{"agent", Item{Kind: KindAgentFailure, RunID: "r9", Source: SourceRef{Cycle: 3}}, "run:r9/cycle:3"},
		{"source chunk", Item{Kind: KindSourceChunk, Source: SourceRef{Path: "a.go", StartLine: 3, EndLine: 9}}, "a.go:3-9"},
		{"unknown", Item{Kind: Kind("other")}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SourceLabel(Hit{Item: tt.item}); got != tt.want {
				t.Errorf("SourceLabel = %q, want %q", got, tt.want)
			}
		})
	}
}
