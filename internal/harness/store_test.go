package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAdoptionRollbackExperienceAndPrivacy(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "private")}
	report := experiment(t, "tool-use", "native")
	now := time.Now()
	if err := store.Update(context.Background(), func(s *State) error {
		if err := s.AddReport(report); err != nil {
			return err
		}
		if err := s.Reflect(report.ID, now); err != nil {
			return err
		}
		if s.Active != nil {
			t.Fatal("reflection activated candidate")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Experiences) != 2 {
		t.Fatal("missing component reflection")
	}
	lessons := Relevant(state.Experiences, "tool-use", testCompat, now)
	if len(lessons) != 2 {
		t.Fatal("applicable lesson missing")
	}
	if len(Relevant(state.Experiences, "coverage", testCompat, now)) != 0 || len(Relevant(state.Experiences, "tool-use", testCompat, now.Add(31*24*time.Hour))) != 0 || len(Relevant(state.Experiences, "tool-use", Compatibility{Provider: "other", Model: "fixture-model"}, now)) != 0 {
		t.Fatal("unrelated or stale experience reused")
	}
	reused, err := Run(context.Background(), ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat, Experiences: state.Experiences})
	if err != nil {
		t.Fatal(err)
	}
	if len(reused.Reused) != 2 || len(reused.Evidence) < 3 {
		t.Fatal("experience was not selectively re-probed")
	}
	for _, operation := range []func(*State) error{
		func(s *State) error { return s.Adopt(report.ID) },
		func(s *State) error {
			snapshot, err := s.ActiveSnapshot(testCompat)
			if err != nil {
				return err
			}
			if len(snapshot.Edits) != 2 {
				t.Fatal("wrong active overlay")
			}
			return s.Revert()
		},
	} {
		if err := store.Update(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	state, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Active != nil {
		t.Fatal("revert did not restore disabled state")
	}
	if err := state.Adopt(report.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ActiveSnapshot(Compatibility{Provider: "other", Model: "fixture-model"}); err == nil {
		t.Fatal("mismatched backend activated")
	}
	state.Disable()
	if state.Active != nil || len(state.Previous) != 0 {
		t.Fatal("disable left active behavior")
	}
	info, err := os.Stat(filepath.Join(store.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal("state is not private")
	}
	info, err = os.Stat(store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatal("state dir is not private")
	}
	data, err := os.ReadFile(filepath.Join(store.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"verified\\n", "one\\ntwo", "required action absent", "raw_prompt", "reasoning", "api_key"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("private payload persisted", forbidden)
		}
	}
}

func TestPersistenceFailureAtomicityAndSchemas(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	report := experiment(t, "tool-use", "native")
	if err := store.Update(context.Background(), func(s *State) error { return s.AddReport(report) }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir, "state.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = store.Update(ctx, func(s *State) error { cancel(); return s.Adopt(report.ID) })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("cancelled mutation persisted")
	}
	err = store.Update(context.Background(), func(s *State) error { s.Active = &Selection{Report: "missing", Snapshot: "missing"}; return nil })
	if err == nil {
		t.Fatal("invalid selection saved")
	}
	if _, err := store.Load(); err != nil {
		t.Fatal("failed mutation broke complete state", err)
	}
	// A writer crash leaves either the previous complete file or the next one;
	// the explicit lock is never auto-stolen from a possibly living writer.
	if err := os.WriteFile(filepath.Join(store.Dir, "write.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(*State) error { return nil }); err == nil {
		t.Fatal("stale/active lock ignored")
	}
	if _, err := store.Load(); err != nil {
		t.Fatal("crash interrupted read recovery")
	}
	if err := os.Remove(filepath.Join(store.Dir, "write.lock")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"schema":999}`, `{"schema":0}`, `{"schema":1,"reports":`, strings.Repeat("x", maxStateBytes+1)} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("corrupt/unknown schema accepted")
		}
		if err := store.Update(context.Background(), func(s *State) error { return s.AddReport(report) }); err == nil {
			t.Fatal("silently replaced corrupt state")
		}
	}
}

func TestReportEvidenceTamperingAndWeakEvidence(t *testing.T) {
	r := experiment(t, "misleading", "native")
	state := State{Schema: SchemaVersion}
	if err := state.AddReport(r); err != nil {
		t.Fatal(err)
	}
	if state.Adopt(r.ID) == nil || state.Reflect(r.ID, time.Now()) == nil {
		t.Fatal("failed candidate adopted or learned")
	}
	r = experiment(t, "unknown", "native")
	if r.Recommendation.Outcome != Inconclusive {
		t.Fatal("shared unsupported assumption rewarded")
	}
	r = experiment(t, "tool-use", "native")
	data, _ := json.Marshal(r)
	var tampered Report
	if err := json.Unmarshal(data, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Evidence[1].Criteria[0].Passed = false
	tampered.ID = tampered.digest()
	if tampered.Validate() == nil {
		t.Fatal("contradictory evidence accepted")
	}
	tampered = r
	tampered.Recommendation.Selected = r.Snapshots[0].ID
	tampered.ID = tampered.digest()
	if tampered.Validate() == nil {
		t.Fatal("unsupported composition accepted")
	}
}

func TestStorageBounds(t *testing.T) {
	r := experiment(t, "tool-use", "native")
	state := State{Schema: SchemaVersion}
	for i := range maxReports + 8 {
		r.Created = r.Created.Add(time.Second)
		r.ID = r.digest()
		if err := state.AddReport(r); err != nil {
			t.Fatal(err)
		}
		if err := state.Reflect(r.ID, time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Reports) != maxReports || len(state.Experiences) > maxExperiences {
		t.Fatal("unbounded experiment storage")
	}
	if err := state.validate(); err != nil {
		t.Fatal(err)
	}
}
