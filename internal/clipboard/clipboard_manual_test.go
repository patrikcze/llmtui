package clipboard

import (
	"context"
	"os"
	"testing"
)

// TestReadImageManual exercises the real system clipboard. It only runs when
// LLMTUI_CLIPBOARD_TEST=1 because it depends on the desktop environment and
// on an image having been copied beforehand.
func TestReadImageManual(t *testing.T) {
	if os.Getenv("LLMTUI_CLIPBOARD_TEST") != "1" {
		t.Skip("set LLMTUI_CLIPBOARD_TEST=1 and copy an image to run")
	}
	data, mime, err := ReadImage(context.Background())
	if err != nil {
		t.Fatalf("ReadImage: %v", err)
	}
	if len(data) == 0 || mime != "image/png" {
		t.Fatalf("got %d bytes, mime %q", len(data), mime)
	}
	t.Logf("read %d bytes (%s)", len(data), mime)
}
