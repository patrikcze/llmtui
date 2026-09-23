package entity

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"regexp"
)

const (
	// randomIDBytes is the amount of entropy (128 bits) minted for each
	// Registry.Publish ID.
	randomIDBytes = 16
	// randomIDChars is the base32-encoded length of randomIDBytes of random
	// data. 128 bits / 5 bits-per-base32-char = 25.6 base32 quanta; Go's
	// encoding/base32 processes input in 5-byte (40-bit) groups and
	// zero-pads a trailing partial group up to the next whole character, so
	// 16 bytes (3 full 5-byte groups = 15 bytes -> 24 chars, plus 1
	// remaining byte) encodes to exactly 24+2 = 26 characters. The last of
	// those 26 characters is NOT fully random: it carries the trailing
	// byte's low 3 bits in its high 3 bits, with its low 2 bits fixed at
	// zero (the zero-padding on that partial quantum) — so the last
	// character is always one of the 8 alphabet symbols at index 0, 4, 8,
	// 12, 16, 20, 24, or 28 (a multiple of 4), never any of the other 24
	// alphabet symbols. This is expected and does not reduce the ID's
	// collision resistance below 128 bits: the 2 padding bits are a fixed,
	// known function of the alphabet and encoding, not a lost bit of
	// entropy — all 128 random bits are still represented, just not each in
	// its own dedicated character slot.
	randomIDChars = 26
)

// randomIDAlphabet is a lowercase, unpadded base32 alphabet so minted IDs
// never need case-folding and never contain "=" padding.
var randomIDEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// idRandomPattern matches only Registry.Publish's random ID shape: it is
// disjoint from idPattern (legacy Registry.Put IDs are a fixed 5-digit
// numeric suffix; random IDs are a fixed 26-char lowercase base32 suffix),
// so the two ID families can never collide or be mistaken for each other.
var idRandomPattern = regexp.MustCompile(`^` + IDPrefix + `[a-z2-7]{` + fmt.Sprint(randomIDChars) + `}$`)

// randomIDSource is the entropy source for newRandomID. Production code
// always uses crypto/rand.Reader; only _test.go files in this package may
// swap it, and must restore it (e.g. via t.Cleanup) when they do.
var randomIDSource io.Reader = rand.Reader

// newRandomID mints a new cross-session-safe entity ID: the IDPrefix
// followed by 26 lowercase base32 characters encoding 128 bits read from
// randomIDSource. A read failure is returned as a genuine error — it never
// silently falls back to a weaker random source or a sequential ID, which
// would risk aliasing IDs minted by a different registry generation or
// process.
func newRandomID() (ID, error) {
	buf := make([]byte, randomIDBytes)
	if _, err := io.ReadFull(randomIDSource, buf); err != nil {
		return "", fmt.Errorf("generate random entity ID: %w", err)
	}
	encoded := randomIDEncoding.EncodeToString(buf)
	if len(encoded) != randomIDChars {
		// Unreachable for a correct encoder/alphabet, but a corrupted
		// encoding must never silently mint a malformed ID.
		return "", fmt.Errorf("random entity ID encoding produced %d characters, want %d", len(encoded), randomIDChars)
	}
	return ID(IDPrefix + encoded), nil
}
