// Package untrusted creates deterministic structural boundaries around data
// that a model may read but must never treat as application instructions.
package untrusted

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Frame encloses content in a matching, collision-free boundary pair. The
// identifier is deterministic so equivalent prompts keep stable cache keys.
// Metadata is quoted so untrusted provenance cannot inject another line.
//
// This is prompt-injection defense in depth, not an authorization boundary;
// controller-side approval and tool policy remain authoritative.
func Frame(kind, source, content string) string {
	kind = strings.TrimSpace(kind)
	source = strings.TrimSpace(source)
	for salt := 0; ; salt++ {
		id := frameID(kind, source, content, salt)
		begin := fmt.Sprintf(
			"<<<LLMTUI_UNTRUSTED_BEGIN id=%s kind=%s source=%s>>>",
			id,
			strconv.Quote(kind),
			strconv.Quote(source),
		)
		end := fmt.Sprintf("<<<LLMTUI_UNTRUSTED_END id=%s>>>", id)
		if strings.Contains(content, begin) || strings.Contains(content, end) {
			continue
		}
		return begin + "\n" + content + "\n" + end
	}
}

func frameID(kind, source, content string, salt int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s", kind, source, salt, content)
	return hex.EncodeToString(h.Sum(nil)[:12])
}
