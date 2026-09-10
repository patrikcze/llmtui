package personalapps

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// echoStdinScript reads all of stdin exactly like mail_bridge.js and returns
// it wrapped in a tiny JSON envelope. It exists to prove osascriptRunner
// actually transports stdin to the child and captures its stdout, without
// depending on Mail.app or a GUI permission prompt.
const echoStdinScript = `
ObjC.import('Foundation');
function readStdin() {
  var data = $.NSFileHandle.fileHandleWithStandardInput.readDataToEndOfFile;
  return $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding).js;
}
JSON.stringify({echoed: readStdin()});
`

const hangScript = `while (true) {}`

// bigOutputScript prints roughly 9MB, comfortably over maxBridgeOutputBytes,
// using Array.join so construction itself stays fast.
const bigOutputScript = `Array(9000001).join('a');`

func TestOsascriptRunnerEchoesStdin(t *testing.T) {
	r := &osascriptRunner{timeout: 10 * time.Second, script: echoStdinScript}
	out, err := r.run(context.Background(), []byte(`hello world`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(out); !strings.Contains(got, "hello world") {
		t.Fatalf("expected echoed stdin in output, got %q", got)
	}
}

func TestOsascriptRunnerTimesOut(t *testing.T) {
	r := &osascriptRunner{timeout: 200 * time.Millisecond, script: hangScript}
	start := time.Now()
	_, err := r.run(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took too long to be observed: %s", elapsed)
	}
}

func TestOsascriptRunnerRejectsOversizedOutput(t *testing.T) {
	r := &osascriptRunner{timeout: 10 * time.Second, script: bigOutputScript}
	_, err := r.run(context.Background(), []byte(`{}`))
	if !errors.Is(err, errBridgeOutputTooLarge) {
		t.Fatalf("expected errBridgeOutputTooLarge, got %v", err)
	}
}

func TestOsascriptRunnerHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &osascriptRunner{timeout: 10 * time.Second, script: hangScript}
	done := make(chan error, 1)
	go func() {
		_, err := r.run(ctx, []byte(`{}`))
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after parent cancellation")
	}
}

func TestBoundedBufferCallsOnOverflowOnce(t *testing.T) {
	var calls int32
	b := &boundedBuffer{max: 10, onOverflow: func() { atomic.AddInt32(&calls, 1) }}
	_, _ = b.Write([]byte("12345"))
	if b.hasOverflowed() {
		t.Fatal("should not have overflowed yet")
	}
	_, _ = b.Write([]byte("67890"))
	if b.hasOverflowed() {
		t.Fatal("exactly at the limit should not overflow")
	}
	_, _ = b.Write([]byte("x"))
	if !b.hasOverflowed() {
		t.Fatal("expected overflow after exceeding max")
	}
	_, _ = b.Write([]byte("more"))
	_, _ = b.Write([]byte("even more"))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onOverflow called %d times, want exactly 1", got)
	}
}

func TestBoundedBufferSummaryTruncates(t *testing.T) {
	b := &boundedBuffer{max: 10_000}
	_, _ = b.Write([]byte(strings.Repeat("x", 1000)))
	s := b.summary()
	if len(s) > 503 { // 500 chars + the ellipsis rune's 3 UTF-8 bytes
		t.Fatalf("summary not truncated: %d bytes", len(s))
	}
}

func TestNewMailBackendReturnsNonNilOnDarwin(t *testing.T) {
	b := NewMailBackend(MailBackendOptions{})
	if b == nil {
		t.Fatal("expected a non-nil MailBackend on darwin")
	}
}

// The bridge is a JXA program, so this source-level contract protects a
// platform-specific behavior that the ordinary fake bridge tests cannot
// execute: draft confirmation must use Mail's stable message id, never an
// English or position-based Drafts-folder heuristic. The latter reports a
// failed mutation after successfully saving a draft on localized accounts.
func TestMailBridgeConfirmsSavedDraftByIdentityNotLocalizedFolderName(t *testing.T) {
	for _, want := range []string{
		"function findSavedDraft(account, nativeID)",
		"messages.whose({ id: parseInt(nativeID, 10) })()",
		"path: [segs[i]]",
		"findSavedDraft(account, nativeID)",
	} {
		if !strings.Contains(mailBridgeScript, want) {
			t.Errorf("mail bridge does not retain identity-based draft confirmation %q", want)
		}
	}
	for _, obsolete := range []string{
		"/^drafts$/i",
		"['Drafts']",
		"msgs[msgs.length - 1]",
	} {
		if strings.Contains(mailBridgeScript, obsolete) {
			t.Errorf("mail bridge still uses localized or position-based confirmation %q", obsolete)
		}
	}
}
