package entity

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func testResourceCandidate(label string) Candidate {
	return Candidate{
		Kind:       KindToolOutput,
		Provenance: Provenance{Source: "tools", Operation: "run_command"},
		Label:      label,
		Trust:      TrustWorkspaceUntrusted,
		Scope:      ScopeSession,
	}
}

type alwaysErrReader struct{}

func (alwaysErrReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated crypto/rand failure")
}

func TestValidKindAcceptsToolOutputAndSearchResult(t *testing.T) {
	if !ValidKind(KindToolOutput) {
		t.Fatal("KindToolOutput should be a valid kind")
	}
	if !ValidKind(KindSearchResult) {
		t.Fatal("KindSearchResult should be a valid kind")
	}
}

func TestPublishAndOpenBodyRoundTrip(t *testing.T) {
	r := NewRegistry(Limits{})
	body := []byte("the quick brown fox jumps over the lazy dog")
	view, err := r.Publish(context.Background(), testResourceCandidate("cmd output"), body)
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != KindToolOutput || view.SizeBytes != int64(len(body)) {
		t.Fatalf("unexpected view: %+v", view)
	}
	if !idRandomPattern.MatchString(view.ID.String()) {
		t.Fatalf("Publish minted a non-random ID: %q", view.ID)
	}

	gotView, lease, err := r.OpenBody(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotView.ID != view.ID {
		t.Fatalf("OpenBody view ID = %q, want %q", gotView.ID, view.ID)
	}
	if lease.Size() != int64(len(body)) {
		t.Fatalf("lease.Size() = %d, want %d", lease.Size(), len(body))
	}
	buf := make([]byte, len(body))
	n, err := lease.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n != len(body) || string(buf[:n]) != string(body) {
		t.Fatalf("ReadAt = %q (%d bytes), want %q", buf[:n], n, body)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("double Close returned error: %v", err)
	}
}

func TestPublishCarriesResourceMetadataIntoView(t *testing.T) {
	r := NewRegistry(Limits{})
	candidate := testResourceCandidate("with metadata")
	candidate.Resource = ResourceMetadata{
		ContentType: "text/plain",
		BodyDigest:  "deadbeef",
		Relation:    "search_of",
		FileVersion: &FileVersion{Path: "notes.txt", Digest: "abc123", SizeBytes: 4, Complete: true},
	}
	view, err := r.Publish(context.Background(), candidate, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	if view.Resource.ContentType != "text/plain" || view.Resource.BodyDigest != "deadbeef" || view.Resource.Relation != "search_of" {
		t.Fatalf("resource metadata not carried through: %+v", view.Resource)
	}
	if view.Resource.FileVersion == nil || view.Resource.FileVersion.Path != "notes.txt" {
		t.Fatalf("file version not carried through: %+v", view.Resource.FileVersion)
	}
}

func TestPublishRejectsInvalidCandidate(t *testing.T) {
	r := NewRegistry(Limits{})
	bad := testResourceCandidate("")
	if _, err := r.Publish(context.Background(), bad, []byte("x")); err == nil {
		t.Fatal("expected empty label to be rejected")
	}
}

func TestPublishAllowsEmptyBody(t *testing.T) {
	r := NewRegistry(Limits{})
	view, err := r.Publish(context.Background(), testResourceCandidate("empty"), []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if view.SizeBytes != 0 {
		t.Fatalf("SizeBytes = %d, want 0", view.SizeBytes)
	}
	_, lease, err := r.OpenBody(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if lease.Size() != 0 {
		t.Fatalf("lease.Size() = %d, want 0", lease.Size())
	}
}

func TestPublishEnforcesPerBodyLimit(t *testing.T) {
	r := NewRegistry(Limits{MaxBodyBytes: 10, MaxTotalBodyBytes: 100})
	if _, err := r.Publish(context.Background(), testResourceCandidate("too big"), make([]byte, 11)); err == nil {
		t.Fatal("expected per-body limit rejection")
	}
	view, err := r.Publish(context.Background(), testResourceCandidate("exact"), make([]byte, 10))
	if err != nil {
		t.Fatalf("expected exact-limit publish to succeed: %v", err)
	}
	if view.SizeBytes != 10 {
		t.Fatalf("SizeBytes = %d, want 10", view.SizeBytes)
	}
}

func TestPublishEvictsOldestUnpinnedBodyUnderTotalBudget(t *testing.T) {
	now := time.Unix(1000, 0)
	r := NewRegistry(Limits{
		MaxBodyBytes:      10,
		MaxTotalBodyBytes: 15,
		Now:               func() time.Time { return now },
	})
	first, err := r.Publish(context.Background(), testResourceCandidate("first"), make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	second, err := r.Publish(context.Background(), testResourceCandidate("second"), make([]byte, 5))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	// Total is already at the 15-byte budget; a third body must evict the
	// oldest unpinned body (first) to fit.
	third, err := r.Publish(context.Background(), testResourceCandidate("third"), make([]byte, 5))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := r.OpenBody(context.Background(), first.ID); err == nil {
		t.Fatal("expected oldest body to be evicted")
	}
	if _, lease, err := r.OpenBody(context.Background(), second.ID); err != nil {
		t.Fatalf("second body should survive: %v", err)
	} else {
		_ = lease.Close()
	}
	if _, lease, err := r.OpenBody(context.Background(), third.ID); err != nil {
		t.Fatalf("third body should be present: %v", err)
	} else {
		_ = lease.Close()
	}
}

func TestPublishFailsWhenAllBodiesArePinned(t *testing.T) {
	r := NewRegistry(Limits{MaxBodyBytes: 10, MaxTotalBodyBytes: 10})
	view, err := r.Publish(context.Background(), testResourceCandidate("only"), make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := r.OpenBody(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()

	if _, err := r.Publish(context.Background(), testResourceCandidate("blocked"), make([]byte, 10)); !errors.Is(err, ErrBodyCapacityExhausted) {
		t.Fatalf("Publish error = %v, want ErrBodyCapacityExhausted", err)
	}
	// The existing pin must be left intact by the failed publish.
	if _, lease2, err := r.OpenBody(context.Background(), view.ID); err != nil {
		t.Fatalf("pinned body should still resolve: %v", err)
	} else {
		_ = lease2.Close()
	}
}

func TestResetReleasesBodiesAndByteCounters(t *testing.T) {
	r := NewRegistry(Limits{MaxBodyBytes: 10, MaxTotalBodyBytes: 10})
	view, err := r.Publish(context.Background(), testResourceCandidate("body"), make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if _, _, err := r.OpenBody(context.Background(), view.ID); err == nil {
		t.Fatal("expected body to be gone after Reset")
	}
	// The byte counter must be back to zero: a full-budget publish must
	// succeed immediately after Reset.
	if _, err := r.Publish(context.Background(), testResourceCandidate("new"), make([]byte, 10)); err != nil {
		t.Fatalf("expected fresh publish after reset to succeed: %v", err)
	}
}

func TestReleaseScopeReleasesOnlyUnpinnedBodiesInScope(t *testing.T) {
	r := NewRegistry(Limits{})
	pinnedCandidate := testResourceCandidate("pinned")
	pinnedCandidate.Scope = ScopeAgentRun
	pinnedCandidate.ScopeID = "run-1"
	pinned, err := r.Publish(context.Background(), pinnedCandidate, []byte("keep"))
	if err != nil {
		t.Fatal(err)
	}

	freeCandidate := testResourceCandidate("free")
	freeCandidate.Scope = ScopeAgentRun
	freeCandidate.ScopeID = "run-1"
	free, err := r.Publish(context.Background(), freeCandidate, []byte("drop"))
	if err != nil {
		t.Fatal(err)
	}

	_, lease, err := r.OpenBody(context.Background(), pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()

	r.ReleaseScope(ScopeAgentRun, "run-1")

	if _, _, err := r.OpenBody(context.Background(), free.ID); err == nil {
		t.Fatal("expected unpinned scoped body to be released")
	}
	if _, lease2, err := r.OpenBody(context.Background(), pinned.ID); err != nil {
		t.Fatalf("pinned scoped body should survive ReleaseScope: %v", err)
	} else {
		_ = lease2.Close()
	}
}

func TestStaleLeaseStaysReadableAfterReset(t *testing.T) {
	r := NewRegistry(Limits{})
	body := []byte("still here")
	view, err := r.Publish(context.Background(), testResourceCandidate("stale"), body)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := r.OpenBody(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}

	r.Reset()

	buf := make([]byte, len(body))
	n, err := lease.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n != len(body) || string(buf[:n]) != string(body) {
		t.Fatalf("stale lease ReadAt = %q, want %q", buf[:n], body)
	}
	if _, _, err := r.OpenBody(context.Background(), view.ID); err == nil {
		t.Fatal("expected fresh OpenBody to fail after Reset")
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close on a stale lease after Reset must stay safe: %v", err)
	}
}

func TestPublishMintsDistinctCrossSessionIDs(t *testing.T) {
	a := NewRegistry(Limits{})
	b := NewRegistry(Limits{})
	viewA, err := a.Publish(context.Background(), testResourceCandidate("a"), []byte("body-a"))
	if err != nil {
		t.Fatal(err)
	}
	viewB, err := b.Publish(context.Background(), testResourceCandidate("b"), []byte("body-b"))
	if err != nil {
		t.Fatal(err)
	}
	if viewA.ID == viewB.ID {
		t.Fatalf("two independently constructed registries minted the same ID: %q", viewA.ID)
	}
	if _, _, err := b.OpenBody(context.Background(), viewA.ID); err == nil {
		t.Fatal("registry B unexpectedly resolved registry A's ID")
	}
	if _, _, err := a.OpenBody(context.Background(), viewB.ID); err == nil {
		t.Fatal("registry A unexpectedly resolved registry B's ID")
	}
}

func TestLegacyAndRandomIDShapesAreDisjoint(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newRandomID()
		if err != nil {
			t.Fatal(err)
		}
		s := id.String()
		if idPattern.MatchString(s) {
			t.Fatalf("random ID %q unexpectedly matched the legacy sequential pattern", s)
		}
		if !idRandomPattern.MatchString(s) {
			t.Fatalf("random ID %q did not match its own pattern", s)
		}
		if _, err := ParseID(s); err != nil {
			t.Fatalf("ParseID rejected a valid random ID %q: %v", s, err)
		}
	}
	legacy := ID("ent_00001")
	if idRandomPattern.MatchString(legacy.String()) {
		t.Fatal("legacy ID unexpectedly matched the random pattern")
	}
	if !legacy.Valid() {
		t.Fatal("legacy ID rejected by Valid()")
	}
}

func TestNewRandomIDLength(t *testing.T) {
	id, err := newRandomID()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(id.String()); got != len(IDPrefix)+randomIDChars {
		t.Fatalf("random ID length = %d, want %d", got, len(IDPrefix)+randomIDChars)
	}
}

func TestNewRandomIDPropagatesSourceFailure(t *testing.T) {
	old := randomIDSource
	randomIDSource = alwaysErrReader{}
	t.Cleanup(func() { randomIDSource = old })

	if _, err := newRandomID(); err == nil {
		t.Fatal("expected newRandomID to fail when its source errors")
	}
}

func TestPublishFailsExplicitlyOnRNGFailureWithoutLeakingQuota(t *testing.T) {
	r := NewRegistry(Limits{MaxBodyBytes: 10, MaxTotalBodyBytes: 10})
	old := randomIDSource
	randomIDSource = alwaysErrReader{}
	t.Cleanup(func() { randomIDSource = old })

	if _, err := r.Publish(context.Background(), testResourceCandidate("boom"), make([]byte, 10)); err == nil {
		t.Fatal("expected Publish to fail explicitly when ID minting fails")
	}

	randomIDSource = old
	// The failed publish's quota reservation must have been released: a
	// full-budget publish should now succeed.
	if _, err := r.Publish(context.Background(), testResourceCandidate("after"), make([]byte, 10)); err != nil {
		t.Fatalf("expected publish to succeed once the RNG recovers: %v", err)
	}
}

func TestOpenBodyRejectsSemanticEntityID(t *testing.T) {
	r := NewRegistry(Limits{})
	view, err := r.Put(testCandidate("semantic payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.OpenBody(context.Background(), view.ID); !errors.Is(err, ErrNotAResourceBody) {
		t.Fatalf("OpenBody error = %v, want ErrNotAResourceBody", err)
	}
}

func TestOpenBodyOnMalformedIDDoesNotPanic(t *testing.T) {
	r := NewRegistry(Limits{})
	if _, _, err := r.OpenBody(context.Background(), ID("ent_not-a-real-shape")); err == nil {
		t.Fatal("expected a malformed ID to be rejected")
	}
}

func TestPublishHonorsCanceledContext(t *testing.T) {
	r := NewRegistry(Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Publish(ctx, testResourceCandidate("canceled"), []byte("x")); err == nil {
		t.Fatal("expected Publish to fail on an already-canceled context")
	}
}

func TestOpenBodyHonorsCanceledContext(t *testing.T) {
	r := NewRegistry(Limits{})
	view, err := r.Publish(context.Background(), testResourceCandidate("body"), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.OpenBody(ctx, view.ID); err == nil {
		t.Fatal("expected OpenBody to fail on an already-canceled context")
	}
}

func BenchmarkOpenBody(b *testing.B) {
	r := NewRegistry(Limits{})
	body := make([]byte, 64*1024)
	view, err := r.Publish(context.Background(), testResourceCandidate("bench"), body)
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, len(body))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, lease, err := r.OpenBody(context.Background(), view.ID)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := lease.ReadAt(buf, 0); err != nil && err != io.EOF {
			b.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
