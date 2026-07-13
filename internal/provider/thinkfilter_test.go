package provider

import "testing"

// feedAll drives a filter with a sequence of deltas and returns the
// concatenated answer and reasoning outputs plus the flush results.
func feedAll(t *testing.T, deltas []string) (answer, reasoning, flushAnswer, unclosed string) {
	t.Helper()
	f := &ThinkFilter{}
	for _, d := range deltas {
		a, r := f.Feed(d)
		answer += a
		reasoning += r
	}
	fa, un := f.Flush()
	return answer, reasoning, fa, un
}

func TestThinkFilterPassthroughWithoutTags(t *testing.T) {
	a, r, fa, un := feedAll(t, []string{"Hello ", "world"})
	if a+fa != "Hello world" || r != "" || un != "" {
		t.Fatalf("got answer=%q reasoning=%q unclosed=%q", a+fa, r, un)
	}
}

func TestThinkFilterStripsLeadingThinkBlock(t *testing.T) {
	a, r, fa, un := feedAll(t, []string{"<think>\nstep one\n</think>\n\nThe answer is 4."})
	if a+fa != "The answer is 4." {
		t.Fatalf("answer = %q", a+fa)
	}
	if r == "" || un != "" {
		t.Fatalf("reasoning=%q unclosed=%q", r, un)
	}
}

func TestThinkFilterHandlesTagsSplitAcrossDeltas(t *testing.T) {
	a, r, fa, _ := feedAll(t, []string{"<thi", "nk>reason", "ing</thi", "nk>done"})
	if a+fa != "done" {
		t.Fatalf("answer = %q", a+fa)
	}
	if r != "reasoning" {
		t.Fatalf("reasoning = %q", r)
	}
}

func TestThinkFilterMidAnswerTagPassesThrough(t *testing.T) {
	a, _, fa, _ := feedAll(t, []string{"Use the <think> tag like this."})
	if a+fa != "Use the <think> tag like this." {
		t.Fatalf("answer = %q", a+fa)
	}
}

func TestThinkFilterLeadingWhitespaceThenThink(t *testing.T) {
	a, r, fa, _ := feedAll(t, []string{"\n <think>hm</think>ok"})
	if a+fa != "ok" || r != "hm" {
		t.Fatalf("answer=%q reasoning=%q", a+fa, r)
	}
}

func TestThinkFilterUnclosedBlockRecoveredOnFlush(t *testing.T) {
	a, _, fa, un := feedAll(t, []string{"<think>all budget spent thinking"})
	if a != "" || fa != "" {
		t.Fatalf("answer should be empty, got %q", a+fa)
	}
	if un != "all budget spent thinking" {
		t.Fatalf("unclosed = %q", un)
	}
}

func TestThinkFilterFlushReturnsUndecidedPrefix(t *testing.T) {
	// A reply that is only "<thi" (stream died mid-tag) must not be lost.
	_, _, fa, _ := feedAll(t, []string{"<thi"})
	if fa != "<thi" {
		t.Fatalf("flush answer = %q", fa)
	}
}
