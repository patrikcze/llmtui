package entity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiskStoragePublishesAtomicallyAndRedactsBeforeWrite(t *testing.T) {
	root := t.TempDir()
	r, err := NewRegistryWithStorage(Limits{MaxBodyBytes: 1024, MaxTotalBodyBytes: 2048}, StorageOptions{Mode: StorageDisk, Root: root, MaxBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	view, err := r.Publish(context.Background(), testResourceCandidate("disk"), []byte("token=secret-value\nvisible"))
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := r.OpenBody(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, lease.Size())
	if _, err := lease.ReadAt(data, 0); err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	if strings.Contains(string(data), "secret-value") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("disk body was not redacted: %q", data)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("spool root entries = %+v", entries)
	}
	files, err := os.ReadDir(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() == ".owner" || file.Name() == ".lock" {
			continue
		}
		info, err := file.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("body mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestDiskStorageQuotaIncludesExistingSpools(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, ".session-abandoned")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, ".owner"), []byte("llmtui\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "abandoned.body"), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistryWithStorage(Limits{MaxBodyBytes: 16, MaxTotalBodyBytes: 16}, StorageOptions{Mode: StorageDisk, Root: root, MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(context.Background(), testResourceCandidate("quota"), make([]byte, 7)); !errors.Is(err, ErrBodyCapacityExhausted) {
		t.Fatalf("Publish error = %v, want aggregate quota exhaustion", err)
	}
}

func TestDiskStorageUnsupportedRootFailsWithoutRegistry(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if registry, err := NewRegistryWithStorage(Limits{}, StorageOptions{Mode: StorageDisk, Root: file, MaxBytes: 1024}); err == nil || registry != nil {
		t.Fatalf("registry=%v err=%v, want setup failure", registry, err)
	}
}

func TestSweepDiskSpoolsOnlyRemovesOwnedOldDirectories(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, ".session-owned")
	foreign := filepath.Join(root, ".session-foreign")
	for _, dir := range []string{owned, foreign} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owned, ".owner"), []byte("llmtui\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{owned, foreign} {
		if err := os.Chtimes(dir, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := SweepDiskSpools(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned spool still exists: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign spool should remain: %v", err)
	}
}

func BenchmarkDiskStorageRead(b *testing.B) {
	r, err := NewRegistryWithStorage(Limits{MaxBodyBytes: 64 * 1024, MaxTotalBodyBytes: 64 * 1024}, StorageOptions{Mode: StorageDisk, Root: b.TempDir(), MaxBytes: 64 * 1024})
	if err != nil {
		b.Fatal(err)
	}
	view, err := r.Publish(context.Background(), testResourceCandidate("bench"), []byte(strings.Repeat("x", 32*1024)))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, lease, err := r.OpenBody(context.Background(), view.ID)
		if err != nil {
			b.Fatal(err)
		}
		buf := make([]byte, 1024)
		_, _ = lease.ReadAt(buf, 0)
		_ = lease.Close()
	}
}
