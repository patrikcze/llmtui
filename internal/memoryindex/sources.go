package memoryindex

import "context"

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
		hits = append(hits, Hit{
			Item: Item{
				Text:  snap.Objective,
				Kind:  KindAgentObjective,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
				},
				Trust: TrustControllerObserved,
			},
			Score: 1.0,
		})
	}

	for _, c := range snap.Criteria {
		hits = append(hits, Hit{
			Item: Item{
				ID:    c.ID,
				Text:  c.Text,
				Kind:  KindAgentCriterion,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
				},
				Tags:  []string{c.Status},
				Trust: TrustControllerObserved,
			},
			Score: 1.0,
		})
	}

	for _, e := range snap.Evidence {
		hits = append(hits, Hit{
			Item: Item{
				Text:  e.Summary,
				Kind:  KindAgentEvidence,
				Scope: ScopeRun,
				RunID: snap.RunID,
				Source: SourceRef{
					RunID: snap.RunID,
					Cycle: e.Cycle,
				},
				Trust: TrustControllerObserved,
			},
			Score: 1.0,
		})
	}

	return hits, nil
}

// EpisodeSource always returns no hits in this phase — episodic memory
// (Phase 3 in the spec) does not exist yet. It exists now purely so the
// Retriever's adapter shape is stable for later phases.
type EpisodeSource struct{}

// Search always returns nil, nil.
func (EpisodeSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	return nil, nil
}
