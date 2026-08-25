package memoryindex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func newTestProjectStore(t *testing.T, root string) *ProjectStore {
	t.Helper()
	project, err := ResolveProject(root)
	if err != nil {
		t.Fatalf("ResolveProject: %v", err)
	}
	store, err := NewProjectStore(filepath.Join(t.TempDir(), "projects"), project.ID)
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}
	return store
}

func TestResolveProjectCanonicalizesSymlinks(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "workspace-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("Symlink: %v", err)
	}

	realProject, err := ResolveProject(realRoot)
	if err != nil {
		t.Fatalf("ResolveProject(real): %v", err)
	}
	linkedProject, err := ResolveProject(linkRoot)
	if err != nil {
		t.Fatalf("ResolveProject(link): %v", err)
	}

	if realProject != linkedProject {
		t.Fatalf("projects differ: real=%+v linked=%+v", realProject, linkedProject)
	}
	if len(realProject.ID) != 64 {
		t.Fatalf("project id length = %d, want 64", len(realProject.ID))
	}
	if strings.Contains(realProject.ID, filepath.Base(realRoot)) {
		t.Fatalf("project id leaks workspace name: %q", realProject.ID)
	}
}

func TestProjectStoreAddLoadRemove(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	kinds := []Kind{
		KindProjectArchitecture,
		KindProjectConvention,
		KindProjectDecision,
	}

	for _, kind := range kinds {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			record, err := store.Add(kind, "  use a modular boundary  ", "design")
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if record.ID == "" || record.ProjectID != store.ProjectID() {
				t.Fatalf("record identity = %+v", record)
			}
			if record.Text != "use a modular boundary" {
				t.Fatalf("record text = %q", record.Text)
			}
			if record.Kind != kind || record.Scope != ScopeProject {
				t.Fatalf("record type = %+v", record)
			}
			if record.Trust != TrustUserAuthored || record.Review != ReviewApproved {
				t.Fatalf("record trust/review = %q/%q", record.Trust, record.Review)
			}
			if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.ContentHash == "" {
				t.Fatalf("record metadata = %+v", record)
			}
		})
	}

	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != len(kinds) {
		t.Fatalf("records = %d, want %d", len(records), len(kinds))
	}
	if err := store.Remove(records[0].ID[:12]); err != nil {
		t.Fatalf("Remove prefix: %v", err)
	}
	records, err = store.Load()
	if err != nil {
		t.Fatalf("Load after Remove: %v", err)
	}
	if len(records) != len(kinds)-1 {
		t.Fatalf("records after remove = %d, want %d", len(records), len(kinds)-1)
	}
	if err := store.Remove("missing"); !errors.Is(err, ErrProjectRecordNotFound) {
		t.Fatalf("Remove missing error = %v, want ErrProjectRecordNotFound", err)
	}
}

func TestProjectStoreRejectsInvalidInput(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	tests := []struct {
		name string
		kind Kind
		text string
	}{
		{name: "empty text", kind: KindProjectDecision, text: "  "},
		{name: "user kind", kind: KindUserPreference, text: "not project memory"},
		{name: "episode kind", kind: KindEpisode, text: "not project memory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.Add(test.kind, test.text); err == nil {
				t.Fatal("Add succeeded, want error")
			}
		})
	}
}

func TestProjectStoreIsolatesWorkspaces(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "projects")
	projectA, err := ResolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := ResolveProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeA, err := NewProjectStore(baseDir, projectA.ID)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewProjectStore(baseDir, projectB.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := storeA.Add(KindProjectDecision, "workspace A uses PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.Add(KindProjectDecision, "workspace B uses SQLite"); err != nil {
		t.Fatal(err)
	}

	recordsA, err := storeA.Load()
	if err != nil {
		t.Fatal(err)
	}
	recordsB, err := storeB.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(recordsA) != 1 || recordsA[0].Text != "workspace A uses PostgreSQL" {
		t.Fatalf("workspace A records = %+v", recordsA)
	}
	if len(recordsB) != 1 || recordsB[0].Text != "workspace B uses SQLite" {
		t.Fatalf("workspace B records = %+v", recordsB)
	}
	if storeA.Path() == storeB.Path() {
		t.Fatalf("isolated stores share path %q", storeA.Path())
	}
}

func TestProjectStoreUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix owner-only mode bits")
	}
	store := newTestProjectStore(t, t.TempDir())
	if _, err := store.Add(KindProjectArchitecture, "hexagonal architecture"); err != nil {
		t.Fatal(err)
	}

	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func TestProjectStoreRedactsLikelySecrets(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	secret := "authorization: Bearer abcdefghijklmnopqrstuvwxyz"
	record, err := store.Add(KindProjectConvention, "Never log "+secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(record.Text, "abcdefghijklmnopqrstuvwxyz") || !strings.Contains(record.Text, "[REDACTED]") {
		t.Fatalf("returned record was not redacted: %q", record.Text)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abcdefghijklmnopqrstuvwxyz") {
		t.Fatal("project store persisted raw secret material")
	}
}

func TestProjectStoreProposalRequiresApproval(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	record, err := store.Propose(KindProjectDecision, "use a write-ahead log")
	if err != nil {
		t.Fatal(err)
	}
	if record.Trust != TrustModelProposed || record.Review != ReviewProposed {
		t.Fatalf("proposal = %+v", record)
	}
	if err := store.Approve(record.ID[:8]); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Trust != TrustModelProposed || records[0].Review != ReviewApproved {
		t.Fatalf("approved records = %+v", records)
	}
}

func TestProjectStorePromotionIsApprovedAndPreservesRunProvenance(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	record, err := store.Promote(
		KindProjectDecision,
		"Verified outcome: use atomic replacement for project memory.",
		"run-abc123",
		3,
		"verifier:passed",
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.Trust != TrustModelProposed || record.Review != ReviewApproved {
		t.Fatalf("promotion trust/review = %+v", record)
	}
	if record.SourceRunID != "run-abc123" || record.SourceCycle != 3 {
		t.Fatalf("promotion provenance = %+v", record)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].SourceRunID != "run-abc123" || loaded[0].SourceCycle != 3 {
		t.Fatalf("loaded promotion = %+v", loaded)
	}
	source := ProjectSource{Store: store}
	hits, err := source.Search(context.Background(), Query{
		Text: "atomic replacement", ProjectID: store.ProjectID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Item.ID != record.ID {
		t.Fatalf("approved promotion was not retrievable: %+v", hits)
	}
	if hits[0].Item.Source.RunID != "run-abc123" || hits[0].Item.Source.Cycle != 3 {
		t.Fatalf("retrieved promotion provenance = %+v", hits[0].Item.Source)
	}
}

func TestProjectStorePromotionRequiresSource(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	if _, err := store.Promote(KindProjectDecision, "outcome", "", 1); err == nil {
		t.Fatal("promotion without run ID should fail")
	}
	if _, err := store.Promote(KindProjectDecision, "outcome", "run-1", 0); err == nil {
		t.Fatal("promotion without a positive cycle should fail")
	}
}

func TestProjectStoreConcurrentAddsRemainValid(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	const count = 16
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Add(KindProjectConvention, "keep updates atomic")
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent Add: %v", err)
		}
	}

	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != count {
		t.Fatalf("records = %d, want %d", len(records), count)
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("atomic write left temporary file %q", entry.Name())
		}
	}
}

func TestProjectStoreRejectsCorruptRecords(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	validRecord := ProjectRecord{
		ID:          "project-0123456789ab",
		Text:        "valid",
		Kind:        KindProjectDecision,
		Scope:       ScopeProject,
		ProjectID:   store.ProjectID(),
		Trust:       TrustUserAuthored,
		Review:      ReviewApproved,
		ContentHash: contentHash("valid"),
	}
	tests := []struct {
		name string
		body any
		raw  string
	}{
		{name: "malformed json", raw: "{"},
		{name: "unsupported version", body: projectFile{Version: 99, ProjectID: store.ProjectID(), Records: []ProjectRecord{validRecord}}},
		{name: "wrong project", body: projectFile{Version: projectStoreVersion, ProjectID: strings.Repeat("a", 64), Records: []ProjectRecord{validRecord}}},
		{name: "invalid kind", body: projectFile{Version: projectStoreVersion, ProjectID: store.ProjectID(), Records: []ProjectRecord{{
			ID: "project-0123456789ab", Text: "bad", Kind: KindEpisode, Scope: ScopeProject,
			ProjectID: store.ProjectID(), Trust: TrustUserAuthored, Review: ReviewApproved, ContentHash: contentHash("bad"),
		}}}},
		{name: "invalid review state", body: projectFile{Version: projectStoreVersion, ProjectID: store.ProjectID(), Records: []ProjectRecord{{
			ID: "project-0123456789ab", Text: "bad", Kind: KindProjectDecision, Scope: ScopeProject,
			ProjectID: store.ProjectID(), Trust: TrustUserAuthored, Review: ReviewUnknown, ContentHash: contentHash("bad"),
		}}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			data := []byte(test.raw)
			if test.body != nil {
				var err error
				data, err = json.Marshal(test.body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(store.Path(), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); !errors.Is(err, ErrCorruptProjectStore) {
				t.Fatalf("Load error = %v, want ErrCorruptProjectStore", err)
			}
		})
	}
}
