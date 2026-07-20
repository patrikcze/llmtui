package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryWriteAndReload(t *testing.T) {
	stores := map[string]Store{
		"memory": NewMemoryStore(),
		"file":   NewFileStore(t.TempDir(), 64*1024, 4),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			run, now := newTestRun(t, DefaultLimits())
			stop := completeCycle(t, run, now, "bounded objective", VerificationResult{Verdict: VerificationPassed, Summary: "passed"})
			if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.ID != run.ID || loaded.Status != DecisionDone || len(loaded.Memory) != 1 {
				t.Fatalf("loaded = %+v", loaded)
			}
		})
	}
}

func TestFileStoreCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, now := newTestRun(t, DefaultLimits())
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "newer.json")
	if err := os.WriteFile(corrupt, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	if err := os.Chtimes(corrupt, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "newer"); !errors.Is(err, ErrCorruptRun) {
		t.Fatalf("Load corrupt error = %v", err)
	}
	latest, err := store.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != run.ID {
		t.Fatalf("latest ID = %q, want %q", latest.ID, run.ID)
	}
}

func TestFileStoreAtomicPermissionsAndBounds(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 1)
	run, now := newTestRun(t, DefaultLimits())
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, run.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
	run2, err := NewRun("run-2", "another", DefaultLimits(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), run2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "run-2.json" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestStoreHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, _ := newTestRun(t, DefaultLimits())
	if err := NewMemoryStore().Save(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRedactsLikelySecrets(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, err := NewRun("secret-run", "use token=supersecret and Authorization: Bearer abcdefghijklmnop", DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.Objective = "do not persist sk-abcdefghijklmnop"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "secret-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"supersecret", "abcdefghijklmnop"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("persisted run leaked %q: %s", secret, data)
		}
	}
	loaded, err := store.Load(context.Background(), "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Request, "[REDACTED]") {
		t.Fatalf("request was not redacted: %q", loaded.Request)
	}
}
