package embedded

import "testing"

func TestAssemblerASCII(t *testing.T) {
	var a Assembler
	if got := a.Push([]byte("hello")); got != "hello" {
		t.Fatalf("Push = %q, want %q", got, "hello")
	}
	if got := a.Push([]byte(" world")); got != " world" {
		t.Fatalf("Push = %q, want %q", got, " world")
	}
	if got := a.Flush(); got != "" {
		t.Fatalf("Flush = %q, want empty", got)
	}
}

func TestAssemblerSplitTwoByteRune(t *testing.T) {
	// "é" = 0xC3 0xA9
	var a Assembler
	full := []byte("caf\xc3\xa9")
	if got := a.Push(full[:len(full)-1]); got != "caf" {
		t.Fatalf("Push(partial) = %q, want %q", got, "caf")
	}
	if got := a.Push(full[len(full)-1:]); got != "é" {
		t.Fatalf("Push(rest) = %q, want %q", got, "é")
	}
	if got := a.Flush(); got != "" {
		t.Fatalf("Flush = %q, want empty", got)
	}
}

func TestAssemblerSplitThreeByteRune(t *testing.T) {
	// "€" = 0xE2 0x82 0xAC
	full := []byte("price: \xe2\x82\xac")
	for split := 1; split < 3; split++ {
		var a Assembler
		lead := len(full) - 3 // offset of the euro sign's first byte
		var got string
		got += a.Push(full[:lead+split])
		got += a.Push(full[lead+split:])
		got += a.Flush()
		if got != "price: €" {
			t.Errorf("split at %d bytes into the rune: got %q, want %q", split, got, "price: €")
		}
	}
}

func TestAssemblerSplitFourByteRune(t *testing.T) {
	// "😀" = 0xF0 0x9F 0x98 0x80
	full := []byte("hi \xf0\x9f\x98\x80!")
	for split := 1; split < 4; split++ {
		var a Assembler
		lead := 3 // offset of the emoji's first byte within full
		var got string
		got += a.Push(full[:lead+split])
		got += a.Push(full[lead+split:])
		got += a.Flush()
		if got != "hi 😀!" {
			t.Errorf("split at %d bytes into the rune: got %q, want %q", split, got, "hi 😀!")
		}
	}
}

func TestAssemblerByteByByte(t *testing.T) {
	full := []byte("go 😀 rocks €5")
	var a Assembler
	var got string
	for _, b := range full {
		got += a.Push([]byte{b})
	}
	got += a.Flush()
	if got != string(full) {
		t.Fatalf("byte-by-byte reassembly = %q, want %q", got, string(full))
	}
}

func TestAssemblerInvalidBytes(t *testing.T) {
	var a Assembler
	got := a.Push([]byte{'a', 0xff, 'b'})
	got += a.Flush()
	if got == "" {
		t.Fatal("expected non-empty output for invalid byte sequence")
	}
	// The invalid byte must be replaced, not silently dropped or left
	// buffered forever, and the valid bytes around it preserved.
	if got[0] != 'a' || got[len(got)-1] != 'b' {
		t.Fatalf("got %q, want to start with 'a' and end with 'b'", got)
	}
}

func TestAssemblerEmptyPushes(t *testing.T) {
	var a Assembler
	if got := a.Push(nil); got != "" {
		t.Fatalf("Push(nil) = %q, want empty", got)
	}
	if got := a.Push([]byte{}); got != "" {
		t.Fatalf("Push(empty) = %q, want empty", got)
	}
	if got := a.Push([]byte("x")); got != "x" {
		t.Fatalf("Push after empties = %q, want %q", got, "x")
	}
	if got := a.Flush(); got != "" {
		t.Fatalf("Flush = %q, want empty", got)
	}
}

func TestAssemblerFlushRemainder(t *testing.T) {
	// Push only the lead byte of a multi-byte rune and never complete it;
	// Flush must still return something rather than dropping the bytes.
	var a Assembler
	if got := a.Push([]byte{0xe2, 0x82}); got != "" {
		t.Fatalf("Push(incomplete) = %q, want empty (should be held back)", got)
	}
	if got := a.Flush(); got == "" {
		t.Fatal("Flush should return the held-back partial rune data, not empty")
	}
}
