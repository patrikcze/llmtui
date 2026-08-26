package clipboard

import (
	"strings"
	"testing"
)

func TestCappedBufferBoundsClipboardOutput(t *testing.T) {
	buffer := cappedBuffer{limit: 5}
	if count, err := buffer.Write([]byte("123456789")); err != nil || count != 9 {
		t.Fatalf("Write = %d, %v", count, err)
	}
	if got := buffer.String(); got != "12345" {
		t.Fatalf("buffer = %q", got)
	}
	if _, err := buffer.Write([]byte(strings.Repeat("x", 100))); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() != 5 {
		t.Fatalf("buffer length = %d", buffer.Len())
	}
}
