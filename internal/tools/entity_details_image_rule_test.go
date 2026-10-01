package tools

import (
	"strings"
	"testing"
)

// TestEntityDetailsAttachedImageNeedsNoLookup mirrors internal/prompt's
// TestAttachedImageIsNotTreatedAsMissingEvidence for the tool's own
// instructions: an image attached to the current message needs no lookup,
// and "report that visual evidence is unavailable" applies only to an
// earlier image that is no longer attached.
func TestEntityDetailsAttachedImageNeedsNoLookup(t *testing.T) {
	rules := EntityDetailsInstructions
	attached := strings.Index(rules, "attached to the current user message is visible")
	fallback := strings.Index(rules, "visual evidence is unavailable")
	if attached < 0 || fallback < 0 || attached > fallback {
		t.Fatalf("attached-image rule must precede the fallback (attached=%d, fallback=%d): %s", attached, fallback, rules)
	}
	if !strings.Contains(rules[attached:fallback], "earlier message that is no longer attached") {
		t.Fatalf("the fallback is not limited to an earlier image: %s", rules)
	}
}
