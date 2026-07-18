package llamart

import (
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// llama.cpp's native log is the only place that names the exact buffer or
// backend that failed when a model load or context allocation goes wrong.
// Silencing it wholesale (the previous behavior) made every native failure an
// opaque "failed to initialize model". Instead, one process-global callback
// captures recent lines into a bounded ring buffer: nothing is ever written
// to stderr (which would corrupt the TUI), and load/init errors can quote
// the relevant tail.
const (
	logRingLines   = 48
	logMaxLineLen  = 300
	logMinKeepChar = 4 // drop empty/noise lines shorter than this
)

var nativeLog struct {
	mu    sync.Mutex
	once  sync.Once
	cb    uintptr
	lines []string
}

// nativeLogCallback returns the process-global capture callback, creating it
// on first use. purego callback slots are permanent, so exactly one is ever
// allocated regardless of how many runtimes load.
func nativeLogCallback() uintptr {
	nativeLog.once.Do(func() {
		// The C signature is ggml_log_callback(level, const char *text, void
		// *user_data); receiving text as *byte lets purego hand us a real
		// pointer, so no vet-unfriendly uintptr round-trip is needed.
		nativeLog.cb = purego.NewCallback(func(level int32, text *byte, data unsafe.Pointer) uintptr {
			line := goStringCapped(text, logMaxLineLen)
			if len(strings.TrimSpace(line)) < logMinKeepChar {
				return 0
			}
			nativeLog.mu.Lock()
			nativeLog.lines = append(nativeLog.lines, strings.TrimRight(line, "\n"))
			if len(nativeLog.lines) > logRingLines {
				nativeLog.lines = nativeLog.lines[len(nativeLog.lines)-logRingLines:]
			}
			nativeLog.mu.Unlock()
			return 0
		})
	})
	return nativeLog.cb
}

// nativeLogReset clears the ring. Load calls it before touching native code
// so a failure never quotes stale lines from a previous, unrelated attempt.
func nativeLogReset() {
	nativeLog.mu.Lock()
	nativeLog.lines = nil
	nativeLog.mu.Unlock()
}

// nativeLogTail returns up to n recent captured native log lines formatted
// for appending to an error message, or "" when nothing was captured. Lines
// that look like routine progress are skipped in favor of warnings/errors
// when any are present.
func nativeLogTail(n int) string {
	nativeLog.mu.Lock()
	lines := make([]string, len(nativeLog.lines))
	copy(lines, nativeLog.lines)
	nativeLog.mu.Unlock()
	if len(lines) == 0 || n <= 0 {
		return ""
	}

	interesting := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(interesting) < n; i-- {
		lower := strings.ToLower(lines[i])
		if strings.Contains(lower, "fail") || strings.Contains(lower, "error") ||
			strings.Contains(lower, "unable") || strings.Contains(lower, "not enough") ||
			strings.Contains(lower, "warn") {
			interesting = append(interesting, strings.TrimSpace(lines[i]))
		}
	}
	if len(interesting) == 0 {
		for i := max(0, len(lines)-n); i < len(lines); i++ {
			interesting = append(interesting, strings.TrimSpace(lines[i]))
		}
	} else {
		// restore chronological order after the reverse scan
		for left, right := 0, len(interesting)-1; left < right; left, right = left+1, right-1 {
			interesting[left], interesting[right] = interesting[right], interesting[left]
		}
	}
	return "; native log: " + strings.Join(interesting, " | ")
}

// goStringCapped copies a NUL-terminated C string of at most maxLen bytes.
// The pointer is only valid for the duration of the log callback, so the
// bytes are copied immediately.
func goStringCapped(text *byte, maxLen int) string {
	if text == nil {
		return ""
	}
	base := unsafe.Pointer(text)
	buf := make([]byte, 0, maxLen)
	for i := 0; i < maxLen; i++ {
		b := *(*byte)(unsafe.Add(base, i))
		if b == 0 {
			break
		}
		buf = append(buf, b)
	}
	return string(buf)
}
