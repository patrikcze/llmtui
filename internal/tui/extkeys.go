package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// Bubble Tea v1 cannot decode modified Enter (Shift+Enter sends the same
// byte as Enter in legacy terminal mode). We therefore enable xterm's
// modifyOtherKeys protocol ourselves and decode the resulting sequences,
// which Bubble Tea surfaces as unknown-CSI messages.
//
// Level 1 is the conservative mode: plain Enter, Esc, Ctrl+C etc. keep
// their legacy encodings; only chords without one (like Shift+Enter) get
// a CSI sequence. Terminals without support silently ignore the enable.
const (
	enableModifyOtherKeys  = "\x1b[>4;1m"
	disableModifyOtherKeys = "\x1b[>4;0m"
)

// extendedKeySeq recovers the raw CSI payload from Bubble Tea's unexported
// unknown-CSI message, whose String() prints "?CSI[<byte> <byte> …]?".
func extendedKeySeq(msg any) (string, bool) {
	s, ok := msg.(fmt.Stringer)
	if !ok {
		return "", false
	}
	str := s.String()
	if !strings.HasPrefix(str, "?CSI[") || !strings.HasSuffix(str, "]?") {
		return "", false
	}
	var b strings.Builder
	for _, field := range strings.Fields(str[5 : len(str)-2]) {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 || n > 255 {
			return "", false
		}
		b.WriteByte(byte(n))
	}
	return b.String(), true
}

// isModifiedEnter reports whether a CSI payload is Enter pressed with a
// modifier. Both xterm forms are handled:
//
//	modifyOtherKeys format 0: 27;<mod>;13~
//	CSI-u / format 1:         13;<mod>u
//
// mod is 1+bitmask(shift=1, alt=2, ctrl=4), so anything ≥ 2 is modified.
func isModifiedEnter(seq string) bool {
	var mod int
	if n, _ := fmt.Sscanf(seq, "27;%d;13~", &mod); n == 1 && strings.HasSuffix(seq, "~") {
		return mod >= 2
	}
	if n, _ := fmt.Sscanf(seq, "13;%du", &mod); n == 1 && strings.HasSuffix(seq, "u") {
		return mod >= 2
	}
	return false
}
