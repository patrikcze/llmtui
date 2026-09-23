package entity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/redact"
)

// StorageOptions selects the optional body backend. Memory is the default;
// disk creates one private, random session spool below Root and never exposes
// a cross-session lookup API.
type StorageOptions struct {
	Mode     string
	Root     string
	MaxBytes int
}

const (
	StorageMemory = "memory"
	StorageDisk   = "disk"
	StorageOff    = "off"
)

// DiskStorageStatus describes the owned spool for diagnostics without
// exposing body content.
type DiskStorageStatus struct {
	Root       string
	SessionDir string
	Bytes      int64
	MaxBytes   int
	Error      error
}

type diskBodyBackend struct {
	mu       sync.Mutex
	root     string
	dir      string
	maxBytes int
	used     int64
	closed   bool
}

func newDiskBodyBackend(opts StorageOptions) (*diskBodyBackend, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = filepath.Join(os.TempDir(), "llmtui-entity-spools")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create entity spool root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect entity spool root: %w", err)
	}
	if err := SweepDiskSpools(root, 24*time.Hour); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, ".session-")
	if err != nil {
		return nil, fmt.Errorf("create entity session spool: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("protect entity session spool: %w", err)
	}
	marker := filepath.Join(dir, ".owner")
	if err := os.WriteFile(marker, []byte(fmt.Sprintf("llmtui\n%d\n", os.Getpid())), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write entity spool marker: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("lock entity session spool: %w", err)
	}
	_ = lock.Close()
	return &diskBodyBackend{root: root, dir: dir, maxBytes: opts.MaxBytes}, nil
}

func (b *diskBodyBackend) prepare(data []byte) []byte {
	// Capture data is already bounded by the producer. Apply the shared
	// best-effort redactor before it can cross the process boundary to disk.
	return []byte(redact.Secrets(string(data)))
}

func (b *diskBodyBackend) put(data []byte) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return "", fmt.Errorf("entity disk spool is closed")
	}
	if b.maxBytes > 0 {
		rootBytes, err := directoryBytes(b.root)
		if err != nil {
			return "", fmt.Errorf("measure entity spool quota: %w", err)
		}
		if rootBytes+int64(len(data)) > int64(b.maxBytes) {
			return "", ErrBodyCapacityExhausted
		}
	}
	name, err := randomDiskName()
	if err != nil {
		return "", err
	}
	tmp, err := os.OpenFile(filepath.Join(b.dir, ".tmp-"+name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create entity spool body: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if err := writeAll(tmp, data); err != nil {
		cleanup()
		return "", fmt.Errorf("write entity spool body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("sync entity spool body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("close entity spool body: %w", err)
	}
	final := filepath.Join(b.dir, name+".body")
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("publish entity spool body: %w", err)
	}
	b.used += int64(len(data))
	return name, nil
}

func (b *diskBodyBackend) open(handle string) (io.ReaderAt, int64, error) {
	if !validDiskHandle(handle) {
		return nil, 0, fmt.Errorf("invalid entity spool handle")
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, 0, fmt.Errorf("entity disk spool is closed")
	}
	path := filepath.Join(b.dir, handle+".body")
	b.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read entity spool body: %w", err)
	}
	return bytesReaderAt(data), int64(len(data)), nil
}

func (b *diskBodyBackend) remove(handle string) {
	if !validDiskHandle(handle) {
		return
	}
	b.mu.Lock()
	path := filepath.Join(b.dir, handle+".body")
	if info, err := os.Stat(path); err == nil {
		b.used -= info.Size()
		if b.used < 0 {
			b.used = 0
		}
	}
	_ = os.Remove(path)
	b.mu.Unlock()
}

func (b *diskBodyBackend) status() DiskStorageStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return DiskStorageStatus{Root: b.root, SessionDir: b.dir, Bytes: b.used, MaxBytes: b.maxBytes}
}

func randomDiskName() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("random entity spool name: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func validDiskHandle(handle string) bool {
	if len(handle) != 32 || strings.ContainsAny(handle, `/\\`) {
		return false
	}
	_, err := hex.DecodeString(handle)
	return err == nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

type byteReaderAt struct{ data []byte }

func bytesReaderAt(data []byte) io.ReaderAt { return &byteReaderAt{data: data} }

func (r *byteReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// SweepDiskSpools removes only old directories carrying llmtui's marker and
// whose exclusive lock can be acquired. Unmarked or live directories remain.
func SweepDiskSpools(root string, olderThan time.Duration) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("scan entity spool root: %w", err)
	}
	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".session-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		marker, err := os.ReadFile(filepath.Join(dir, ".owner"))
		if err != nil || !strings.HasPrefix(string(marker), "llmtui\n") {
			continue
		}
		// A live owner keeps its PID in the marker. Abandoned sessions may
		// leave the lock file behind after a crash, so PID liveness is checked
		// before taking the cleanup lock; a live session is never swept.
		var pid int
		_, _ = fmt.Sscanf(string(marker), "llmtui\n%d", &pid)
		if pid > 0 && processAlive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, ".lock"))
		lock, err := os.OpenFile(filepath.Join(dir, ".sweep-lock"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			continue
		}
		_ = lock.Close()
		_ = os.RemoveAll(dir)
	}
	return nil
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
