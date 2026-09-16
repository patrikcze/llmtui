package entity

import (
	"testing"
	"time"
)

func testCandidate(payload string) Candidate {
	return Candidate{
		Kind:       KindFile,
		Provenance: Provenance{Source: "workspace", Operation: "read_file"},
		Label:      "notes.txt",
		Trust:      TrustWorkspaceUntrusted,
		Scope:      ScopeSession,
		Payload:    payload,
		Preview:    payload,
	}
}

func TestIDIsOpaqueAndStrictlyValidated(t *testing.T) {
	for _, raw := range []string{"ent_00001", "ent_99999"} {
		id, err := ParseID(raw)
		if err != nil || id.String() != raw || !id.Valid() {
			t.Fatalf("ParseID(%q) = %q, %v", raw, id, err)
		}
	}
	for _, raw := range []string{"", "ent_1", "ent_000001", "ent_00001/secret", "ent_00001 "} {
		if _, err := ParseID(raw); err == nil {
			t.Errorf("ParseID(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestRegistryProgressiveDisclosureAndExpansionBudget(t *testing.T) {
	r := NewRegistry(Limits{MaxFullExpansions: 1})
	first, err := r.Put(testCandidate("sensitive-looking workspace data"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Level != LevelMinimal || first.Payload != "" || first.Digest == "" {
		t.Fatalf("minimal view exposed unexpected data: %+v", first)
	}
	minimal := r.Resolve(first.ID.String(), LevelMinimal)
	if minimal.Status != StatusOK || minimal.View.Payload != "" || minimal.View.Preview == "" {
		t.Fatalf("minimal resolution = %+v", minimal)
	}
	identifier := r.Resolve(first.ID.String(), LevelIdentifier)
	if identifier.Status != StatusOK || identifier.View.Preview != "" || identifier.View.Payload != "" {
		t.Fatalf("identifier resolution = %+v", identifier)
	}
	full := r.Resolve(first.ID.String(), LevelFull)
	if full.Status != StatusOK || full.View.Payload != "sensitive-looking workspace data" {
		t.Fatalf("full resolution = %+v", full)
	}
	second, err := r.Put(testCandidate("second payload"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(second.ID.String(), LevelFull); got.Status != StatusDetailNotAvailable {
		t.Fatalf("second full resolution = %+v, want expansion budget error", got)
	}
	r.BeginRequest()
	if got := r.Resolve(second.ID.String(), LevelFull); got.Status != StatusOK {
		t.Fatalf("full expansion did not reset: %+v", got)
	}
}

func TestRegistryBoundsExpiryAndDeterministicReset(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRegistry(Limits{
		MaxEntities:     2,
		MaxPayloadBytes: 5,
		MaxTotalPayload: 10,
		Now:             func() time.Time { return now },
	})
	one, err := r.Put(testCandidate("123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Resolve(one.ID.String(), LevelFull).View.Payload) > 5 {
		t.Fatal("payload exceeded per-entity bound")
	}
	now = now.Add(time.Second)
	two, err := r.Put(testCandidate("two"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	three, err := r.Put(testCandidate("three"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(one.ID.String(), LevelMinimal); got.Status != StatusNotFound {
		t.Fatalf("oldest entity survived eviction: %+v", got)
	}
	if r.Resolve(two.ID.String(), LevelMinimal).Status != StatusOK || r.Resolve(three.ID.String(), LevelMinimal).Status != StatusOK {
		t.Fatal("newer entities were not retained")
	}
	expiring, err := r.Put(Candidate{Kind: KindCollection, Provenance: Provenance{Source: "test"}, Label: "expiring", Trust: TrustControllerObserved, Scope: ScopeTurn, ScopeID: "turn-1", Preview: "soon", ExpiresAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if got := r.Resolve(expiring.ID.String(), LevelMinimal); got.Status != StatusExpired {
		t.Fatalf("expired entity = %+v", got)
	}
	r.Reset()
	newView, err := r.Put(testCandidate("new"))
	if err != nil {
		t.Fatal(err)
	}
	if newView.ID == one.ID {
		t.Fatal("reset reused an old opaque ID")
	}
}

func TestRegistryScopeReleaseAndRetention(t *testing.T) {
	r := NewRegistry(Limits{MaxEntities: 1})
	view, err := r.Put(Candidate{Kind: KindMCPResult, Provenance: Provenance{Source: "mcp:test"}, Label: "run result", Trust: TrustMCPUntrusted, Scope: ScopeAgentRun, ScopeID: "run-1", Payload: "result"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Retain(view.ID) {
		t.Fatal("Retain rejected live entity")
	}
	r.ReleaseScope(ScopeAgentRun, "run-1")
	if r.Resolve(view.ID.String(), LevelMinimal).Status != StatusOK {
		t.Fatal("retained entity was released")
	}
	r.Release(view.ID)
	r.ReleaseScope(ScopeAgentRun, "run-1")
	if r.Resolve(view.ID.String(), LevelMinimal).Status != StatusNotFound {
		t.Fatal("released scope entity survived")
	}
}

func TestTruncateBytesRespectsByteLimit(t *testing.T) {
	for max := 1; max <= 5; max++ {
		got, truncated := truncateBytes("žlutý kůň", max)
		if !truncated || len(got) > max {
			t.Fatalf("truncateBytes max=%d got %q (%d bytes)", max, got, len(got))
		}
	}
}
