package entity

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// bodyBackend stores bounded body bytes and hands back read-only handles.
// It is Registry's own storage abstraction, not part of this package's
// public API. Disk backing is opt-in through NewRegistryWithStorage.
type bodyBackend interface {
	// prepare applies backend-specific sensitivity handling before quota
	// reservation and persistence. The memory backend returns the input.
	prepare(data []byte) []byte
	// put stores data and returns an opaque handle for later open/remove
	// calls. Implementations must copy data rather than retain the caller's
	// slice, since the caller may reuse or mutate it after put returns.
	put(data []byte) (handle string, err error)
	// open returns a ReaderAt over the stored bytes and their size. The
	// returned ReaderAt must remain valid to read even if remove is called
	// for a different handle, or (for the memory backend specifically) even
	// after remove is called for this same handle — Registry's pin
	// contract is what actually prevents a live lease's handle from being
	// removed during normal eviction; open's independence from the backend
	// map is a defense-in-depth property, documented precisely for the
	// harder case of Registry.Reset, which intentionally does not wait for
	// open leases (see Registry.Reset's doc comment).
	open(handle string) (io.ReaderAt, int64, error)
	// remove drops the stored bytes for handle. Removing an unknown or
	// already-removed handle is a no-op, not an error.
	remove(handle string)
}

// memoryBodyBackend stores published bodies as independent byte-slice
// copies in memory.
type memoryBodyBackend struct {
	mu     sync.Mutex
	seq    uint64
	blocks map[string][]byte
}

func (b *memoryBodyBackend) prepare(data []byte) []byte { return data }

func newMemoryBodyBackend() *memoryBodyBackend {
	return &memoryBodyBackend{blocks: make(map[string][]byte)}
}

func (b *memoryBodyBackend) put(data []byte) (string, error) {
	stored := make([]byte, len(data))
	copy(stored, data)
	b.mu.Lock()
	b.seq++
	handle := fmt.Sprintf("mem-%d", b.seq)
	b.blocks[handle] = stored
	b.mu.Unlock()
	return handle, nil
}

func (b *memoryBodyBackend) open(handle string) (io.ReaderAt, int64, error) {
	b.mu.Lock()
	data, ok := b.blocks[handle]
	b.mu.Unlock()
	if !ok {
		return nil, 0, fmt.Errorf("body storage handle %q is not present", handle)
	}
	// bytes.NewReader wraps the stored slice directly (no further copy);
	// the slice itself was already an independent copy made in put, and
	// this backend never mutates a stored slice in place, so the returned
	// reader stays valid to read even after a later remove(handle) drops
	// it from the blocks map — only Go's garbage collector reclaims the
	// bytes, once every reader referencing them is gone.
	return bytes.NewReader(data), int64(len(data)), nil
}

func (b *memoryBodyBackend) remove(handle string) {
	b.mu.Lock()
	delete(b.blocks, handle)
	b.mu.Unlock()
}
