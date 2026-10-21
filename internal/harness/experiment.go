package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type Outcome string

const (
	KeepBase         Outcome = "KEEP_BASE"
	AdoptInvariant   Outcome = "ADOPT_INVARIANT"
	AdoptDomain      Outcome = "ADOPT_DOMAIN"
	ComposeSupported Outcome = "COMPOSE_SUPPORTED_EDITS"
	Inconclusive     Outcome = "INCONCLUSIVE"
)

type Recommendation struct {
	Outcome  Outcome  `json:"outcome"`
	Selected string   `json:"selected"`
	Reasons  []string `json:"reasons"`
}

type ProposalRecord struct {
	Scope    string `json:"scope"`
	Edit     Edit   `json:"edit,omitempty"`
	Evidence string `json:"evidence"`
	Status   string `json:"status"`
}

// Report is content-addressed; no original task, model rationale or tool body
// can enter it. Simulation provenance accompanies every recommendation.
type Report struct {
	Schema           int              `json:"schema"`
	ID               string           `json:"id"`
	Created          time.Time        `json:"created"`
	Scenario         string           `json:"scenario"`
	Protocol         string           `json:"protocol"`
	Mode             string           `json:"mode"`
	Compatibility    Compatibility    `json:"compatibility"`
	Snapshots        []Snapshot       `json:"snapshots"`
	Evidence         []Evidence       `json:"evidence"`
	Proposals        []ProposalRecord `json:"proposals"`
	Recommendation   Recommendation   `json:"recommendation"`
	ProviderRequests int              `json:"provider_requests"`
	Tokens           int              `json:"tokens_estimated_or_reported"`
	Reused           []string         `json:"reused_experiences"`
}

func hashText(s string) string  { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (r Report) digest() string { r.ID = ""; b, _ := json.Marshal(r); return hashText(string(b))[:24] }

type ExperimentOptions struct {
	Scenario      string
	Protocol      string
	Compatibility Compatibility
	Proposer      *Proposer // nil selects deterministic fixture proposals
	Experiences   []Experience
}

// Run performs k=1 sequentially: baseline, two proposals, two probes and at
// most one combined probe. Only the proposer can use a configured provider.
// No state is written here, including after cancellation or partial failure.
func Run(ctx context.Context, opts ExperimentOptions) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	report := Report{Schema: SchemaVersion, Created: now, Scenario: opts.Scenario, Protocol: opts.Protocol, Mode: "restricted-fixture-simulation", Compatibility: opts.Compatibility}
	base, err := NewSnapshot("", nil, "fixture", opts.Compatibility, now)
	if err != nil {
		return Report{}, err
	}
	baseline, err := runFixture(ctx, opts.Scenario, opts.Protocol, base)
	if err != nil {
		return Report{}, err
	}
	report.Snapshots = append(report.Snapshots, base)
	report.Evidence = append(report.Evidence, baseline)
	lessons := Relevant(opts.Experiences, opts.Scenario, opts.Compatibility, now)
	for _, l := range lessons {
		report.Reused = append(report.Reused, l.ID)
	}
	scopes := []string{"invariant", "domain"}
	for _, scope := range scopes {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		edit := fixtureEdit(opts.Scenario, scope)
		source := "fixture"
		record := ProposalRecord{Scope: scope, Evidence: baseline.ID, Status: "validated"}
		if opts.Proposer != nil && !baseline.Passed {
			// Reserve the maximum output plus bounded input before every call.
			if report.ProviderRequests >= 2 || report.Tokens+maxProposalRequestTokens > maxExperimentTokens {
				return Report{}, errors.New("harness provider budget exhausted")
			}
			proposal, tokens, err := opts.Proposer.propose(ctx, scope, baseline, lessons)
			report.ProviderRequests++
			report.Tokens += tokens
			if ctx.Err() != nil {
				return Report{}, ctx.Err()
			}
			if report.Tokens > maxExperimentTokens {
				return Report{}, errors.New("harness token budget exhausted")
			}
			if err != nil {
				record.Status = "rejected_invalid_or_unsupported"
				report.Proposals = append(report.Proposals, record)
				continue
			}
			edit = proposal.Edit
			source = "provider"
		}
		// Experiences guide proposal selection, and are always re-probed. They
		// never provide task facts or become production instructions on retrieval.
		if opts.Proposer == nil {
			for _, l := range lessons {
				if Catalog()[l.Correction].Scope == scope {
					edit = l.Correction
					break
				}
			}
		}
		record.Edit = edit
		report.Proposals = append(report.Proposals, record)
		candidate, err := NewSnapshot(base.ID, []Edit{edit}, source, opts.Compatibility, now)
		if err != nil {
			return Report{}, err
		}
		evidence, err := runFixture(ctx, opts.Scenario, opts.Protocol, candidate)
		if err != nil {
			return Report{}, err
		}
		report.Snapshots = append(report.Snapshots, candidate)
		report.Evidence = append(report.Evidence, evidence)
	}
	report.Recommendation = compare(report)
	// Compose only independently passing, disjoint components. The merged
	// snapshot must itself pass a fourth fresh probe before being recommended.
	if !baseline.Passed && baseline.Oracle && len(report.Evidence) == 3 && report.Evidence[1].Passed && report.Evidence[2].Passed && compatible(report.Snapshots[1], report.Snapshots[2]) {
		edits := append(slices.Clone(report.Snapshots[1].Edits), report.Snapshots[2].Edits...)
		merged, err := NewSnapshot(base.ID, edits, "composition", opts.Compatibility, now)
		if err != nil {
			return Report{}, err
		}
		combined, err := runFixture(ctx, opts.Scenario, opts.Protocol, merged)
		if err != nil {
			return Report{}, err
		}
		report.Snapshots = append(report.Snapshots, merged)
		report.Evidence = append(report.Evidence, combined)
		if combined.Passed {
			report.Recommendation = Recommendation{Outcome: ComposeSupported, Selected: merged.ID, Reasons: []string{
				"simulation-only; each component passed independently and the combined fixture oracle passed",
				string(edits[0]) + ": " + report.Evidence[1].ID + " versus " + baseline.ID,
				string(edits[1]) + ": " + report.Evidence[2].ID + " versus " + baseline.ID,
				"combined: " + combined.ID,
			}}
		}
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	report.ID = report.digest()
	return report, report.Validate()
}

const maxExperimentTokens = 24000
const maxProposalRequestTokens = 10000

func fixtureEdit(scenario, scope string) Edit {
	if scope == "domain" {
		if scenario == "recovery" {
			return FixtureRecovery
		}
		if scenario == "coverage" {
			return ReadProcedure
		}
		return ArtifactProcedure
	}
	switch scenario {
	case "coverage":
		return CompleteRead
	case "recovery":
		return RecoverTyped
	case "premature":
		return VerifyArtifact
	default:
		return ExecuteTools
	}
}

func compatible(a, b Snapshot) bool {
	for _, c := range a.Components {
		if slices.Contains(b.Components, c) {
			return false
		}
	}
	return len(a.Edits)+len(b.Edits) <= maxEdits
}

func compare(r Report) Recommendation {
	base := r.Evidence[0]
	result := Recommendation{Outcome: KeepBase, Selected: base.Snapshot, Reasons: []string{"no demonstrated oracle improvement; retain the baseline"}}
	if !base.Oracle {
		result.Outcome = Inconclusive
		result.Reasons = []string{"no independent correctness oracle; semantic agreement is insufficient"}
		return result
	}
	if base.Passed {
		return result
	}
	for i := 1; i < len(r.Evidence); i++ {
		e := r.Evidence[i]
		if !e.Oracle || !e.Passed || e.DeterministicFailure || e.FailedTests > 0 || len(e.Criteria) != len(base.Criteria) {
			continue
		}
		coverage := true
		for j, c := range base.Criteria {
			if e.Criteria[j].ID != c.ID || !e.Criteria[j].Passed {
				coverage = false
			}
		}
		if !coverage {
			continue
		}
		outcome := AdoptInvariant
		for _, s := range r.Snapshots {
			if s.ID == e.Snapshot && len(s.Edits) > 0 && Catalog()[s.Edits[0]].Scope == "domain" {
				outcome = AdoptDomain
			}
		}
		result = Recommendation{Outcome: outcome, Selected: e.Snapshot, Reasons: []string{fmt.Sprintf("simulation-only: %s passes all fixture criteria; baseline %s fails", e.ID, base.ID)}}
		return result
	}
	return result
}

func (r Report) Validate() error {
	if r.Schema != SchemaVersion || r.ID != r.digest() || !validID(r.ID) || r.Created.IsZero() || r.Mode != "restricted-fixture-simulation" || !slices.Contains(Scenarios(), r.Scenario) || (r.Protocol != "native" && r.Protocol != "fenced") {
		return errors.New("invalid or unsupported experiment report")
	}
	if len(r.Snapshots) < 1 || len(r.Snapshots) > 4 || len(r.Evidence) != len(r.Snapshots) || len(r.Proposals) != 2 || r.ProviderRequests < 0 || r.ProviderRequests > 2 || r.Tokens < 0 || r.Tokens > maxExperimentTokens {
		return errors.New("invalid experiment budgets")
	}
	selected := false
	for i, s := range r.Snapshots {
		if err := s.Validate(); err != nil {
			return err
		}
		e := r.Evidence[i]
		if s.Compatibility != r.Compatibility || e.Snapshot != s.ID || e.ID != hashText(s.ID + e.Fixture + r.Protocol)[:24] || e.Fixture != fixtureVersion+":"+r.Scenario || e.InitialDigest != hashText(fixtureVersion+fixtureInput) || e.Protocol != r.Protocol {
			return errors.New("incomparable experiment evidence")
		}
		if i == 0 {
			if s.Parent != "" || len(s.Edits) != 0 {
				return errors.New("invalid baseline")
			}
		} else if s.Parent != r.Snapshots[0].ID {
			return errors.New("invalid candidate ancestry")
		}
		if e.ToolCalls < 0 || e.ToolCalls > 6 || len(e.Receipts) != e.ToolCalls || len(e.Criteria) == 0 || len(e.Criteria) > 4 {
			return errors.New("invalid fixture receipts")
		}
		passed := e.Oracle && !e.DeterministicFailure && e.FailedTests == 0 && e.InvalidCalls == 0
		for _, c := range e.Criteria {
			passed = passed && c.Passed
		}
		if e.Passed != passed {
			return errors.New("unsupported fixture success claim")
		}
		if s.ID == r.Recommendation.Selected {
			selected = true
			if i > 0 && (!e.Passed || !e.Oracle || r.Evidence[0].Passed) {
				return errors.New("recommendation lacks improvement evidence")
			}
		}
	}
	if !selected {
		return errors.New("missing recommended snapshot")
	}
	expected := compare(r)
	if r.Recommendation.Outcome == ComposeSupported {
		if len(r.Snapshots) != 4 {
			return errors.New("composition is missing probe evidence")
		}
		union := append(slices.Clone(r.Snapshots[1].Edits), r.Snapshots[2].Edits...)
		slices.Sort(union)
		if !slices.Equal(union, r.Snapshots[len(r.Snapshots)-1].Edits) {
			return errors.New("composition changed unsupported edits")
		}
		if len(r.Evidence) != 4 || !r.Evidence[1].Passed || !r.Evidence[2].Passed || !r.Evidence[3].Passed || !compatible(r.Snapshots[1], r.Snapshots[2]) || r.Recommendation.Selected != r.Snapshots[3].ID {
			return errors.New("composition lacks independent and combined evidence")
		}
	} else if r.Recommendation.Outcome != expected.Outcome || r.Recommendation.Selected != expected.Selected {
		return errors.New("unsupported comparison outcome")
	}
	return nil
}
