package memoryindex

import (
	"fmt"
	"strconv"
	"time"

	"github.com/patrikcze/llmtui/internal/untrusted"
)

// ShortID abbreviates a project identifier for display and provenance labels.
func ShortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// SourceLabel is the provenance label a hit carries in Active Context. It is
// defined here, beside the token estimate that measures it, so the label the
// prompt renders and the label the budget counts cannot drift apart.
func SourceLabel(hit Hit) string {
	label := "unknown"
	switch hit.Item.Kind {
	case KindUserPreference:
		label = "user"
	case KindProjectArchitecture, KindProjectConvention, KindProjectDecision:
		label = "project:" + ShortID(hit.Item.ProjectID)
		if hit.Item.Source.RunID != "" {
			label += "/run:" + hit.Item.Source.RunID
			if hit.Item.Source.Cycle > 0 {
				label += fmt.Sprintf("/cycle:%d", hit.Item.Source.Cycle)
			}
		}
	case KindEpisode:
		label = "session:" + hit.Item.SessionID
	case KindAgentObjective, KindAgentCriterion, KindAgentFailure, KindAgentEvidence:
		label = "run:" + hit.Item.RunID
		if hit.Item.Source.Cycle > 0 {
			label += fmt.Sprintf("/cycle:%d", hit.Item.Source.Cycle)
		}
	case KindSourceChunk:
		label = fmt.Sprintf("%s:%d-%d", hit.Item.Source.Path, hit.Item.Source.StartLine, hit.Item.Source.EndLine)
	}
	return label
}

// framingBytes is the size of everything Active Context wraps around a hit's
// text: the record element with its provenance attributes, and the untrusted
// boundary pair. It mirrors prompt.formatActiveContext; a test in the tui
// package renders real prompts and fails if the two diverge.
func framingBytes(hit Hit) int {
	tag, frameKind := "memory", "active_memory"
	if hit.Item.Kind == KindSourceChunk {
		tag, frameKind = "source", "active_source"
	}
	updated := "unknown"
	if !hit.Item.UpdatedAt.IsZero() {
		updated = hit.Item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	label := SourceLabel(hit)
	open := fmt.Sprintf("\n  <%s id=%s kind=%s scope=%s source=%s trust=%s updated=%s>\n",
		tag, strconv.Quote(hit.Item.ID), strconv.Quote(string(hit.Item.Kind)), strconv.Quote(string(hit.Item.Scope)),
		strconv.Quote(label), strconv.Quote(string(hit.Item.Trust)), strconv.Quote(updated))
	closing := "\n  </" + tag + ">"
	// Frame with empty content is exactly the boundary overhead: begin line,
	// newline, newline, end line.
	return len(open) + len(untrusted.Frame(frameKind, label+":"+hit.Item.ID, "")) + len(closing)
}
