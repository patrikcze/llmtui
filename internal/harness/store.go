package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/patrikcze/llmtui/internal/atomicfile"
)

const maxStateBytes = 2 << 20
const maxReports = 24
const maxExperiences = 32

type Selection struct {
	Report   string `json:"report"`
	Snapshot string `json:"snapshot"`
}

type State struct {
	Schema      int          `json:"schema"`
	Reports     []Report     `json:"reports"`
	Active      *Selection   `json:"active,omitempty"`
	Previous    []*Selection `json:"previous,omitempty"`
	Experiences []Experience `json:"experiences"`
}

// Store keeps one atomically replaced private file, separate from config,
// agent runs and ordinary memory. Cross-process mutation is fail-fast locked.
type Store struct{ Dir string }

func (s Store) Load() (State, error) {
	state := State{Schema: SchemaVersion}
	f, err := os.Open(filepath.Join(s.Dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return state, err
	}
	if len(b) > maxStateBytes {
		return state, errors.New("harness state size limit exceeded")
	}
	if err := decodeStrict(b, &state); err != nil {
		return State{}, err
	}
	return state, state.validate()
}

func (s Store) Update(ctx context.Context, change func(*State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	lock := filepath.Join(s.Dir, "write.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("harness state locked; after a crash, remove write.lock only after confirming no harness writer is running")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lock)
		return err
	}
	defer os.Remove(lock)
	state, err := s.Load()
	if err != nil {
		return fmt.Errorf("load harness state: %w", err)
	}
	if err := change(&state); err != nil {
		return err
	}
	if err := state.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxStateBytes {
		return errors.New("harness state size limit exceeded")
	}
	// No asynchronous callbacks may commit a cancelled experiment.
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(s.Dir, "state.json"), b, 0o600)
}

func (s *State) AddReport(report Report) error {
	if err := report.Validate(); err != nil {
		return err
	}
	for _, r := range s.Reports {
		if r.ID == report.ID {
			return nil
		}
	}
	s.Reports = append(s.Reports, report)
	if len(s.Reports) > maxReports {
		for i, r := range s.Reports {
			protected := s.Active != nil && s.Active.Report == r.ID
			for _, p := range s.Previous {
				protected = protected || (p != nil && p.Report == r.ID)
			}
			if !protected {
				s.Reports = slices.Delete(s.Reports, i, i+1)
				break
			}
		}
	}
	s.Experiences = slices.DeleteFunc(s.Experiences, func(e Experience) bool { _, err := s.Report(e.Report); return err != nil })
	return nil
}

func (s State) Report(id string) (Report, error) {
	for _, r := range s.Reports {
		if r.ID == id {
			return r, nil
		}
	}
	return Report{}, errors.New("harness experiment not found")
}

func (s State) selection(sel *Selection) (Snapshot, error) {
	if sel == nil {
		return Snapshot{}, errors.New("experimental harness is disabled")
	}
	r, err := s.Report(sel.Report)
	if err != nil {
		return Snapshot{}, err
	}
	if r.Recommendation.Selected != sel.Snapshot || r.Recommendation.Outcome == KeepBase || r.Recommendation.Outcome == Inconclusive {
		return Snapshot{}, errors.New("snapshot is not a validated recommendation")
	}
	for _, snapshot := range r.Snapshots {
		if snapshot.ID == sel.Snapshot {
			return snapshot, snapshot.Validate()
		}
	}
	return Snapshot{}, errors.New("active harness snapshot missing")
}

func (s *State) Adopt(reportID string) error {
	r, err := s.Report(reportID)
	if err != nil {
		return err
	}
	next := &Selection{Report: reportID, Snapshot: r.Recommendation.Selected}
	if _, err := s.selection(next); err != nil {
		return err
	}
	if s.Active != nil && *s.Active == *next {
		return nil
	}
	s.Previous = append(s.Previous, s.Active)
	if len(s.Previous) > 8 {
		s.Previous = s.Previous[1:]
	}
	s.Active = next
	return nil
}

func (s *State) Revert() error {
	if len(s.Previous) == 0 {
		return errors.New("no previous harness selection")
	}
	s.Active = s.Previous[len(s.Previous)-1]
	s.Previous = s.Previous[:len(s.Previous)-1]
	return nil
}

func (s *State) Disable() { s.Active = nil; s.Previous = nil }

// ActiveSnapshot is read only on explicit chat opt-in. Compatibility is exact;
// switching model/provider/runtime later suppresses the overlay in the TUI.
func (s State) ActiveSnapshot(compat Compatibility) (Snapshot, error) {
	snapshot, err := s.selection(s.Active)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Compatibility != compat {
		return Snapshot{}, errors.New("active harness provider/model/runtime mismatch; re-evaluate before reuse")
	}
	return snapshot, nil
}

func (s State) validate() error {
	if s.Schema != SchemaVersion {
		return errors.New("unsupported harness state schema; no implicit migration")
	}
	if len(s.Reports) > maxReports || len(s.Previous) > 8 || len(s.Experiences) > maxExperiences {
		return errors.New("harness storage budget exceeded")
	}
	seen := map[string]bool{}
	for _, r := range s.Reports {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.ID] {
			return errors.New("duplicate experiment")
		}
		seen[r.ID] = true
	}
	if s.Active != nil {
		if _, err := s.selection(s.Active); err != nil {
			return err
		}
	}
	for _, p := range s.Previous {
		if p != nil {
			if _, err := s.selection(p); err != nil {
				return err
			}
		}
	}
	seen = map[string]bool{}
	for _, e := range s.Experiences {
		if err := s.validateExperience(e); err != nil {
			return err
		}
		if seen[e.ID] {
			return errors.New("duplicate experience")
		}
		seen[e.ID] = true
	}
	return nil
}

// Experience is a structured equivalent of EXPERIENCE.md. Its prose is
// application-owned catalog text; reflection never stores model explanations.
type Experience struct {
	Schema        int           `json:"schema"`
	ID            string        `json:"id"`
	Domain        string        `json:"domain"`
	Trigger       string        `json:"trigger"`
	Problem       string        `json:"problem"`
	Correction    Edit          `json:"correction"`
	Component     string        `json:"component"`
	Report        string        `json:"report"`
	Baseline      string        `json:"baseline"`
	Candidate     string        `json:"candidate"`
	Evidence      string        `json:"evidence"`
	Compatibility Compatibility `json:"compatibility"`
	Provenance    string        `json:"provenance"`
	Confidence    string        `json:"confidence"`
	Caveat        string        `json:"caveat"`
	Created       time.Time     `json:"created"`
	Expires       time.Time     `json:"expires"`
}

func (s *State) Reflect(reportID string, now time.Time) error {
	r, err := s.Report(reportID)
	if err != nil {
		return err
	}
	if r.Recommendation.Outcome == KeepBase || r.Recommendation.Outcome == Inconclusive {
		return errors.New("no validated correction to retain")
	}
	for i, snapshot := range r.Snapshots {
		if snapshot.ID != r.Recommendation.Selected {
			continue
		}
		for _, edit := range snapshot.Edits {
			e := Experience{Schema: SchemaVersion, ID: hashText(r.ID + string(edit))[:24], Domain: r.Scenario, Trigger: r.Scenario, Problem: "fixture_oracle_failed", Correction: edit, Component: Catalog()[edit].Component, Report: r.ID, Baseline: r.Snapshots[0].ID, Candidate: snapshot.ID, Evidence: r.Evidence[i].ID, Compatibility: r.Compatibility, Provenance: "deterministic-fixture-oracle-v1", Confidence: "simulation_only", Caveat: "simulation-only; revalidate on each task; no real-model efficacy measured", Created: now.UTC(), Expires: now.Add(30 * 24 * time.Hour).UTC()}
			s.Experiences = slices.DeleteFunc(s.Experiences, func(old Experience) bool { return old.ID == e.ID || !old.Expires.After(now) })
			s.Experiences = append(s.Experiences, e)
		}
	}
	if len(s.Experiences) > maxExperiences {
		s.Experiences = s.Experiences[len(s.Experiences)-maxExperiences:]
	}
	return nil
}

func (s State) validateExperience(e Experience) error {
	if e.Schema != SchemaVersion || e.ID != hashText(e.Report + string(e.Correction))[:24] || e.Created.IsZero() || !e.Expires.After(e.Created) || e.Expires.Sub(e.Created) > 30*24*time.Hour {
		return errors.New("invalid experience metadata")
	}
	r, err := s.Report(e.Report)
	if err != nil {
		return err
	}
	if e.Domain != r.Scenario || e.Trigger != r.Scenario || e.Problem != "fixture_oracle_failed" || e.Compatibility != r.Compatibility || e.Baseline != r.Snapshots[0].ID || e.Candidate != r.Recommendation.Selected || e.Provenance != "deterministic-fixture-oracle-v1" || e.Confidence != "simulation_only" || e.Component != Catalog()[e.Correction].Component || e.Caveat != "simulation-only; revalidate on each task; no real-model efficacy measured" {
		return errors.New("experience provenance mismatch")
	}
	for i, c := range r.Snapshots {
		if c.ID == e.Candidate && slices.Contains(c.Edits, e.Correction) && r.Evidence[i].Passed && e.Evidence == r.Evidence[i].ID {
			return nil
		}
	}
	return errors.New("experience lacks passing evidence")
}

// Relevant is exact, bounded retrieval. A domain name is a trigger, never an
// assertion that a new workspace or real model shares the fixture's facts.
func Relevant(entries []Experience, domain string, compat Compatibility, now time.Time) []Experience {
	var out []Experience
	for i := len(entries) - 1; i >= 0 && len(out) < 3; i-- {
		e := entries[i]
		_, known := Catalog()[e.Correction]
		if known && e.Schema == SchemaVersion && e.Domain == domain && e.Trigger == domain && e.Compatibility == compat && e.Provenance == "deterministic-fixture-oracle-v1" && e.Confidence == "simulation_only" && !e.Created.After(now) && e.Expires.After(now) {
			out = append(out, e)
		}
	}
	return out
}
