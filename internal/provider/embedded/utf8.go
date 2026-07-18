package embedded

import "strings"

// Assembler accumulates raw byte pieces (as produced token-by-token by a
// native tokenizer) and releases only complete, valid UTF-8 text, buffering
// a trailing partial multibyte rune (up to 3 bytes) across calls so a rune
// split across two native callbacks is never emitted as mojibake.
type Assembler struct {
	buf []byte
}

// Push appends p to the internal buffer and returns the longest prefix that
// forms complete UTF-8 text. Any trailing bytes that look like the
// (possibly incomplete) start of a multibyte rune are held back for the
// next call. If the held-back buffer grows past the longest possible
// encoded rune (4 bytes) without becoming complete — i.e. it can never
// become valid, not merely incomplete — it is flushed through
// strings.ToValidUTF8 so the buffer never grows unbounded.
func (a *Assembler) Push(p []byte) string {
	if len(p) == 0 {
		// Nothing new to contribute; whatever is buffered stays held back.
		return ""
	}
	a.buf = append(a.buf, p...)
	n := len(a.buf)

	// Walk back at most 3 bytes to find the start of a lead byte (a byte
	// that is not a UTF-8 continuation byte, 10xxxxxx).
	leadIdx := 0
	found := false
	for i := n - 1; i >= 0 && i >= n-3; i-- {
		if a.buf[i]&0xC0 != 0x80 {
			leadIdx = i
			found = true
			break
		}
	}
	if !found {
		// The trailing up-to-3 bytes are all continuation bytes with no
		// lead byte in range: something is malformed. Hold back at most 3
		// bytes; the growth guard below prevents unbounded buffering.
		leadIdx = n - 3
		if leadIdx < 0 {
			leadIdx = 0
		}
	}

	want := runeLength(a.buf[leadIdx])
	have := n - leadIdx

	var complete, pending []byte
	if have >= want {
		complete = a.buf
		pending = nil
	} else {
		complete = a.buf[:leadIdx]
		pending = a.buf[leadIdx:]
	}

	if len(pending) >= 4 {
		// Can never become valid by waiting longer; flush it now.
		complete = a.buf
		pending = nil
	}

	out := strings.ToValidUTF8(string(complete), "�")
	if len(pending) > 0 {
		a.buf = append([]byte(nil), pending...)
	} else {
		a.buf = nil
	}
	return out
}

// runeLength returns how many bytes a UTF-8 rune starting with lead should
// occupy, per the leading byte's high bits. Invalid lead bytes report 1 so
// they are treated as already-complete (and thus subject to
// strings.ToValidUTF8 replacement) rather than held back forever.
func runeLength(lead byte) int {
	switch {
	case lead < 0x80:
		return 1
	case lead>>5 == 0b110:
		return 2
	case lead>>4 == 0b1110:
		return 3
	case lead>>3 == 0b11110:
		return 4
	default:
		return 1
	}
}

// Flush drains any remaining buffered bytes, coercing invalid or permanently
// incomplete trailing fragments to the UTF-8 replacement character rather
// than dropping them silently.
func (a *Assembler) Flush() string {
	if len(a.buf) == 0 {
		return ""
	}
	out := strings.ToValidUTF8(string(a.buf), "�")
	a.buf = nil
	return out
}
