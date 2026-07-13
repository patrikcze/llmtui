package provider

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// ThinkFilter separates a leaked leading <think>…</think> block from
// assistant answer deltas. Reasoning models served through a misconfigured
// backend chat template emit chain-of-thought inline in content instead of
// the dedicated reasoning_content/thinking channel; unfiltered it would be
// rendered as the answer, stored in session history, re-sent to the backend
// on every later turn, and cached. Only a block that opens the reply (after
// optional whitespace) is treated as reasoning — a literal "<think>" later
// in an answer passes through untouched.
type ThinkFilter struct {
	state    thinkState
	pending  strings.Builder // undecided prefix, or a held-back partial close tag
	thinkBuf strings.Builder // full reasoning text, kept for unclosed-block recovery
}

type thinkState int

const (
	thinkDeciding thinkState = iota
	thinkInside
	thinkPassthrough
)

// Feed consumes one streamed delta and returns the portion that is visible
// answer text and the portion that is reasoning. Either may be empty while
// the filter buffers a possible partial tag.
func (f *ThinkFilter) Feed(delta string) (answer, reasoning string) {
	if f.state == thinkPassthrough {
		return delta, ""
	}
	f.pending.WriteString(delta)
	if f.state == thinkDeciding {
		buf := f.pending.String()
		trimmed := strings.TrimLeft(buf, " \t\r\n")
		switch {
		case trimmed == "":
			return "", ""
		case strings.HasPrefix(trimmed, thinkOpen):
			f.state = thinkInside
			f.pending.Reset()
			f.pending.WriteString(trimmed[len(thinkOpen):])
		case strings.HasPrefix(thinkOpen, trimmed):
			// Could still become "<think>"; keep buffering.
			return "", ""
		default:
			f.state = thinkPassthrough
			f.pending.Reset()
			return buf, ""
		}
	}
	buf := f.pending.String()
	if i := strings.Index(buf, thinkClose); i >= 0 {
		reasoning = buf[:i]
		f.thinkBuf.WriteString(reasoning)
		answer = strings.TrimLeft(buf[i+len(thinkClose):], "\n")
		f.pending.Reset()
		f.state = thinkPassthrough
		return answer, reasoning
	}
	// Hold back the longest suffix that could be the start of "</think>"
	// split across deltas; release the rest as reasoning immediately so the
	// activity indicator keeps moving.
	hold := 0
	for n := min(len(buf), len(thinkClose)-1); n > 0; n-- {
		if strings.HasPrefix(thinkClose, buf[len(buf)-n:]) {
			hold = n
			break
		}
	}
	reasoning = buf[:len(buf)-hold]
	f.thinkBuf.WriteString(reasoning)
	f.pending.Reset()
	f.pending.WriteString(buf[len(buf)-hold:])
	return "", reasoning
}

// Flush ends the stream. It returns any buffered undecided text as answer,
// and — when the stream ended inside an unclosed think block — the complete
// reasoning text so the caller can salvage it instead of showing nothing.
func (f *ThinkFilter) Flush() (answer, unclosedReasoning string) {
	switch f.state {
	case thinkDeciding:
		answer = f.pending.String()
	case thinkInside:
		f.thinkBuf.WriteString(f.pending.String())
		unclosedReasoning = strings.TrimSpace(f.thinkBuf.String())
	}
	f.pending.Reset()
	f.state = thinkPassthrough
	return answer, unclosedReasoning
}
