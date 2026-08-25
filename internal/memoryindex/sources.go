package memoryindex

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/patrikcze/llmtui/internal/history"
)

// AgentRunSnapshot is a point-in-time view of one live AgentRun, projected
// into memoryindex hits by AgentRunSource.
type AgentRunSnapshot struct {
	RunID     string
	Objective string
	Criteria  []AgentCriterionSnapshot
	Evidence  []AgentEvidenceSnapshot
}

// AgentCriterionSnapshot is one verification criterion of an AgentRun.
type AgentCriterionSnapshot struct {
	ID, Text, Status string
}

// AgentEvidenceSnapshot is one piece of evidence gathered during an
// AgentRun.
type AgentEvidenceSnapshot struct {
	Cycle   int
	Kind    string
	Source  string
	Summary string
	Success bool
}

// AgentRunSource projects a snapshot of one live AgentRun into hits. It is
// a real, tested Source implementation but — per the plan's global
// constraints — is never registered into the Retriever composition the app
// builds; nothing in this task wires it into the TUI. A later phase owns
// that wiring.
type AgentRunSource struct {
	Snapshot func() (AgentRunSnapshot, bool) // false = no active run
}

const maxAgentRunEvidenceHits = 16

// Search returns hits projecting the active AgentRun's objective, criteria,
// and evidence. It returns nil, nil when there is no active run, or when
// q.RunID is set and does not match the active run's RunID.
func (s AgentRunSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	snap, ok := s.Snapshot()
	if !ok {
		return nil, nil
	}
	if q.RunID != "" && q.RunID != snap.RunID {
		return nil, nil
	}

	var hits []Hit

	if snap.Objective != "" {
		id := snap.RunID + ":objective"
		hits = append(hits, Hit{
			Item: Item{
				ID:    id,
				Text:  snap.Objective,
				Kind:  KindAgentObjective,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
				},
				Trust:       TrustControllerObserved,
				ContentHash: contentHash(snap.Objective),
			},
			Score: 1.0,
			Why:   "current controller-owned agent objective",
		})
	}

	for _, c := range snap.Criteria {
		hits = append(hits, Hit{
			Item: Item{
				ID:    snap.RunID + ":criterion:" + c.ID,
				Text:  c.Text,
				Kind:  KindAgentCriterion,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
				},
				Tags:        []string{"status:" + c.Status},
				Trust:       TrustControllerObserved,
				ContentHash: contentHash(c.Text),
			},
			Score: 1.0,
			Why:   "pinned agent criterion with controller-owned status",
		})
	}

	evidence := slices.Clone(snap.Evidence)
	if len(evidence) > maxAgentRunEvidenceHits {
		evidence = evidence[len(evidence)-maxAgentRunEvidenceHits:]
	}
	for index, e := range evidence {
		kind := KindAgentEvidence
		if !e.Success {
			kind = KindAgentFailure
		}
		id := fmt.Sprintf("%s:evidence:%d:%d", snap.RunID, e.Cycle, index)
		hits = append(hits, Hit{
			Item: Item{
				ID:    id,
				Text:  e.Summary,
				Kind:  kind,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
					Cycle: e.Cycle,
				},
				Tags: []string{
					"kind:" + e.Kind,
					"source:" + e.Source,
					fmt.Sprintf("success:%t", e.Success),
				},
				Trust:       TrustControllerObserved,
				ContentHash: contentHash(e.Summary),
			},
			Score: 1.0,
			Why:   "bounded controller-observed agent evidence",
		})
	}

	return hits, nil
}

// EpisodeSource exposes compact summaries from saved sessions. Full
// transcripts are never projected into memoryindex items.
type EpisodeSource struct {
	Dir  string
	TopK int
}

// Search returns lexical matches from prior sessions in the exact current
// project. The current session and sessions without project-scoped episodes
// are excluded.
func (s EpisodeSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Dir) == "" || q.ProjectID == "" || !queryAllowsKind(q.Kinds, KindEpisode) {
		return nil, nil
	}
	queryTerms := lexicalTerms(q.Text)
	if len(queryTerms) == 0 {
		return nil, nil
	}
	metas, err := history.List(s.Dir)
	if err != nil {
		return nil, err
	}

	hits := make([]Hit, 0, len(metas))
	for _, meta := range metas {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if meta.Name == q.SessionID {
			continue
		}
		session, err := history.Load(s.Dir, meta.Name)
		if err != nil || session.Episode == nil || session.Episode.ProjectID != q.ProjectID {
			continue
		}
		text := session.Episode.RetrievalText()
		matched := matchingTerms(queryTerms, lexicalTerms(text))
		if len(matched) == 0 {
			continue
		}
		hits = append(hits, Hit{
			Item: Item{
				ID:          meta.Name,
				Text:        text,
				Summary:     text,
				Kind:        KindEpisode,
				Scope:       ScopeSession,
				ProjectID:   session.Episode.ProjectID,
				SessionID:   meta.Name,
				Source:      SourceRef{SessionID: meta.Name},
				Tags:        []string{session.Episode.Status, session.Episode.Provider, session.Episode.Model},
				CreatedAt:   session.Episode.SavedAt,
				UpdatedAt:   session.Episode.SavedAt,
				Trust:       TrustModelProposed,
				ContentHash: contentHash(text),
			},
			Score:        float64(len(matched)) / float64(len(queryTerms)),
			MatchedTerms: matched,
			Why: fmt.Sprintf(
				"matched %d query term(s) in saved episode: %s",
				len(matched),
				strings.Join(matched, ", "),
			),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool { return hitLess(hits[i], hits[j]) })
	limit := s.TopK
	if limit <= 0 {
		limit = 10
	}
	if q.TopK > 0 && q.TopK < limit {
		limit = q.TopK
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
