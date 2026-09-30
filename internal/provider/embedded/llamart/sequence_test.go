package llamart

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

type kvCall struct {
	op     string
	seq    llama.SeqId
	p0, p1 llama.Pos
	tokens int
}

// fakeKV records every native memory/decode call a Runtime makes.
type fakeKV struct {
	calls       []kvCall
	seqRmResult bool
	seqRmErr    error
	clearErr    error
	decodeCode  int32
}

func (f *fakeKV) native() kvNative {
	return kvNative{
		seqRm: func(_ llama.Memory, seq llama.SeqId, p0, p1 llama.Pos) (bool, error) {
			f.calls = append(f.calls, kvCall{op: "seq_rm", seq: seq, p0: p0, p1: p1})
			return f.seqRmResult, f.seqRmErr
		},
		clear: func(llama.Memory, bool) error {
			f.calls = append(f.calls, kvCall{op: "clear"})
			return f.clearErr
		},
		decodeBatch: func(_ llama.Context, seq llama.SeqId, tokens []llama.Token, start llama.Pos) (int32, error) {
			f.calls = append(f.calls, kvCall{op: "decode", seq: seq, p0: start, tokens: len(tokens)})
			return f.decodeCode, nil
		},
	}
}

func (f *fakeKV) ops() []string {
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.op)
	}
	return out
}

func tokenRange(n int) []llama.Token {
	tokens := make([]llama.Token, n)
	for i := range tokens {
		tokens[i] = llama.Token(i + 1)
	}
	return tokens
}

func newSequenceRuntime(fake *fakeKV, nCtx, cached int) *Runtime {
	r := New()
	r.kv = fake.native()
	r.nCtx = nCtx
	r.batchSize = 16
	r.nSeqMax = contextSequences
	r.kvTokens = tokenRange(cached)
	return r
}

func TestPlanIsolatedPrompt(t *testing.T) {
	tests := []struct {
		name                         string
		nCtx, cached, prompt, maxTok int
		contaminated                 bool
		wantMax                      int
		wantEvict, wantErr           bool
	}{
		{name: "fits beside conversation", nCtx: 16384, cached: 6000, prompt: 1500, maxTok: 1024, wantMax: 1024},
		{name: "exactly fills the free cells", nCtx: 4096, cached: 2000, prompt: 1072, maxTok: 1024, wantMax: 1024},
		{name: "reply room short by one evicts", nCtx: 4096, cached: 2001, prompt: 1072, maxTok: 1024, wantMax: 1024, wantEvict: true},
		{name: "full conversation evicts", nCtx: 8192, cached: 8192, prompt: 700, maxTok: 256, wantMax: 256, wantEvict: true},
		{name: "unbounded reply needs minimum room", nCtx: 4096, cached: 3000, prompt: 500, maxTok: 0, wantMax: 3596, wantEvict: true},
		{name: "unbounded reply uses free cells", nCtx: 16384, cached: 6000, prompt: 1000, maxTok: 0, wantMax: 9384},
		{name: "image embeddings always evict", nCtx: 16384, cached: 0, prompt: 700, maxTok: 256, contaminated: true, wantMax: 256, wantEvict: true},
		{name: "prompt larger than context fails", nCtx: 2048, cached: 0, prompt: 2048, maxTok: 16, wantEvict: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxNew, evict, err := planIsolatedPrompt(tt.nCtx, tt.cached, tt.contaminated, tt.prompt, tt.maxTok)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tt.wantErr)
			}
			if evict != tt.wantEvict {
				t.Fatalf("evict = %t, want %t", evict, tt.wantEvict)
			}
			if err == nil && maxNew != tt.wantMax {
				t.Fatalf("maxNew = %d, want %d", maxNew, tt.wantMax)
			}
		})
	}
}

func TestControlPromptKeepsConversationPrefix(t *testing.T) {
	fake := &fakeKV{seqRmResult: true}
	r := newSequenceRuntime(fake, 16384, 6000)
	before := slices.Clone(r.kvTokens)

	maxNew, err := r.prepareControlPrompt(40, 256)
	if err != nil || maxNew != 256 {
		t.Fatalf("prepareControlPrompt = %d, %v", maxNew, err)
	}
	if err := r.decodeControlPrompt(context.Background(), tokenRange(40), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.decodeControl(context.Background(), []llama.Token{7}, 40); err != nil {
		t.Fatal(err)
	}
	if err := r.releaseControlSequence(); err != nil {
		t.Fatal(err)
	}

	want := []kvCall{
		{op: "decode", seq: controlSequence, p0: 0, tokens: 16},
		{op: "decode", seq: controlSequence, p0: 16, tokens: 16},
		{op: "decode", seq: controlSequence, p0: 32, tokens: 8},
		{op: "decode", seq: controlSequence, p0: 40, tokens: 1},
		{op: "seq_rm", seq: controlSequence, p0: -1, p1: -1},
	}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("native calls = %+v\nwant %+v", fake.calls, want)
	}
	if !slices.Equal(r.kvTokens, before) {
		t.Fatal("control request changed the conversation's cached tokens")
	}

	// The next conversation request still reuses its whole cached prefix.
	fake.calls = nil
	pending, err := r.preparePrompt(append(slices.Clone(before), 9001, 9002))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || len(fake.calls) != 0 {
		t.Fatalf("pending = %d tokens, native calls = %+v; want 2 new tokens and no memory changes", len(pending), fake.calls)
	}
}

func TestControlPromptEvictsConversationWhenFull(t *testing.T) {
	fake := &fakeKV{seqRmResult: true}
	r := newSequenceRuntime(fake, 8192, 8000)
	r.kvContaminated = false

	maxNew, err := r.prepareControlPrompt(700, 256)
	if err != nil || maxNew != 256 {
		t.Fatalf("prepareControlPrompt = %d, %v", maxNew, err)
	}
	if !slices.Equal(fake.ops(), []string{"clear"}) {
		t.Fatalf("native calls = %v, want one full clear", fake.ops())
	}
	if len(r.kvTokens) != 0 {
		t.Fatalf("kvTokens = %d after eviction, want 0", len(r.kvTokens))
	}
}

func TestControlPromptClearsImageContaminatedMemory(t *testing.T) {
	fake := &fakeKV{seqRmResult: true}
	r := newSequenceRuntime(fake, 16384, 0)
	r.kvContaminated = true

	if _, err := r.prepareControlPrompt(700, 256); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.ops(), []string{"clear"}) || r.kvContaminated {
		t.Fatalf("native calls = %v, contaminated = %t; image memory must be cleared", fake.ops(), r.kvContaminated)
	}
}

func TestReleaseControlSequenceFallsBackToFullClear(t *testing.T) {
	for _, tt := range []struct {
		name    string
		removed bool
		err     error
	}{
		{name: "rejected", removed: false},
		{name: "errored", removed: false, err: errors.New("native failure")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeKV{seqRmResult: tt.removed, seqRmErr: tt.err}
			r := newSequenceRuntime(fake, 16384, 500)
			if err := r.releaseControlSequence(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(fake.ops(), []string{"seq_rm", "clear"}) {
				t.Fatalf("native calls = %v, want seq_rm then clear", fake.ops())
			}
			if len(r.kvTokens) != 0 {
				t.Fatal("conversation tokens must be dropped once memory was cleared")
			}
		})
	}
}

func TestReleaseControlSequenceReportsClearFailure(t *testing.T) {
	fake := &fakeKV{seqRmResult: false, clearErr: errors.New("clear failed")}
	r := newSequenceRuntime(fake, 16384, 500)
	if err := r.releaseControlSequence(); err == nil {
		t.Fatal("release must report a failed fallback clear")
	}
}

func TestDecodeControlReportsStatus(t *testing.T) {
	fake := &fakeKV{decodeCode: 1}
	r := newSequenceRuntime(fake, 16384, 0)
	if err := r.decodeControl(context.Background(), tokenRange(3), 0); err == nil {
		t.Fatal("non-zero llama_decode status must be an error")
	}
}

func TestPreparePromptTrimsConversationSequence(t *testing.T) {
	fake := &fakeKV{seqRmResult: true}
	r := newSequenceRuntime(fake, 16384, 10)
	prompt := append(tokenRange(6), 99, 100)

	pending, err := r.preparePrompt(prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := []kvCall{{op: "seq_rm", seq: conversationSequence, p0: 6, p1: -1}}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("native calls = %+v, want %+v", fake.calls, want)
	}
	if len(pending) != 2 || len(r.kvTokens) != 6 {
		t.Fatalf("pending = %d, cached = %d; want 2 and 6", len(pending), len(r.kvTokens))
	}
}

func TestApplyContextOptionsEnablesControlSequence(t *testing.T) {
	params := llama.ContextParams{}
	if err := applyContextOptions(&params, embedded.Options{}, 4096, 512, embedded.KVCacheTypeF16, embedded.KVCacheTypeF16, embedded.FlashAttentionAuto); err != nil {
		t.Fatal(err)
	}
	if params.NSeqMax != contextSequences || params.KVUnified != 1 {
		t.Fatalf("NSeqMax = %d, KVUnified = %d; want %d sequences over a unified KV buffer", params.NSeqMax, params.KVUnified, contextSequences)
	}
}
