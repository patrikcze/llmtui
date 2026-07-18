package llamart

import (
	"strings"
	"testing"
)

func withInjectedNativeLog(t *testing.T, lines []string) {
	t.Helper()
	nativeLog.mu.Lock()
	saved := nativeLog.lines
	nativeLog.lines = append([]string{}, lines...)
	nativeLog.mu.Unlock()
	t.Cleanup(func() {
		nativeLog.mu.Lock()
		nativeLog.lines = saved
		nativeLog.mu.Unlock()
	})
}

func TestNativeLogTailPrefersFailureLines(t *testing.T) {
	withInjectedNativeLog(t, []string{
		"llama_model_loader: loaded meta data",
		"ggml_metal_init: found device: Apple M4",
		"ggml_backend_metal_buffer_type_alloc_buffer: failed to allocate buffer of size 9216.00 MiB",
		"llama_init_from_model: failed to initialize the context",
	})
	tail := nativeLogTail(3)
	if !strings.Contains(tail, "failed to allocate buffer") {
		t.Fatalf("tail %q does not quote the decisive allocation failure", tail)
	}
	if strings.Contains(tail, "loaded meta data") {
		t.Fatalf("tail %q includes routine noise despite failures being present", tail)
	}
}

func TestNativeLogTailChronologicalAndEmpty(t *testing.T) {
	withInjectedNativeLog(t, nil)
	if tail := nativeLogTail(3); tail != "" {
		t.Fatalf("empty ring produced tail %q", tail)
	}
	withInjectedNativeLog(t, []string{"error: first", "error: second"})
	tail := nativeLogTail(3)
	if first, second := strings.Index(tail, "first"), strings.Index(tail, "second"); first == -1 || second == -1 || first > second {
		t.Fatalf("tail %q is not chronological", tail)
	}
}

func TestNativeLogTailFallsBackToRecentLines(t *testing.T) {
	withInjectedNativeLog(t, []string{"plain line one", "plain line two"})
	tail := nativeLogTail(1)
	if !strings.Contains(tail, "plain line two") {
		t.Fatalf("tail %q should fall back to the most recent lines", tail)
	}
}
