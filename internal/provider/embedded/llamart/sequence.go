package llamart

import (
	"context"
	"errors"
	"fmt"

	"github.com/hybridgroup/yzma/pkg/llama"
)

// The context holds two llama.cpp sequences over one unified KV buffer.
// Sequence 0 carries the conversation and its reusable token prefix
// (Runtime.kvTokens). Sequence 1 is scratch space for isolated control
// requests (provider.ChatRequest.Isolated: task contract, verifier), so they
// no longer overwrite the conversation prefix the next executor request
// reuses. The KV buffer is unified, so neither sequence is limited to half of
// n_ctx; both share its cells, and sequence 1 is emptied after every request.
const (
	conversationSequence llama.SeqId = 0
	controlSequence      llama.SeqId = 1
	contextSequences                 = 2
)

// minIsolatedReply is the reply room an isolated request without its own
// max_tokens must have beside the cached conversation before it may run in
// the control sequence instead of evicting the conversation.
const minIsolatedReply = 1024

// kvNative holds the llama.cpp memory and decode calls used by text prompt
// bookkeeping, injectable so the sequence logic is testable without native
// libraries (see visionNative for the same pattern).
type kvNative struct {
	seqRm       func(llama.Memory, llama.SeqId, llama.Pos, llama.Pos) (bool, error)
	clear       func(llama.Memory, bool) error
	decodeBatch func(ctx llama.Context, seq llama.SeqId, tokens []llama.Token, start llama.Pos) (int32, error)
}

func defaultKVNative() kvNative {
	return kvNative{
		seqRm:       llama.MemorySeqRm,
		clear:       llama.MemoryClear,
		decodeBatch: decodeSequenceBatch,
	}
}

// decodeSequenceBatch decodes tokens into one sequence at explicit positions,
// requesting logits only for the final token (the one the sampler reads).
func decodeSequenceBatch(lctx llama.Context, seq llama.SeqId, tokens []llama.Token, start llama.Pos) (int32, error) {
	batch := llama.BatchInit(int32(len(tokens)), 0, 1)
	defer func() { _ = llama.BatchFree(batch) }() // llama_batch_free returns void
	seqIDs := []llama.SeqId{seq}
	for i, token := range tokens {
		if err := batch.Add(token, start+llama.Pos(i), seqIDs, i == len(tokens)-1); err != nil {
			return 0, fmt.Errorf("build sequence %d batch: %w", seq, err)
		}
	}
	return llama.Decode(lctx, batch)
}

// planIsolatedPrompt decides whether an isolated prompt fits in the control
// sequence beside the cached conversation. It returns the generation budget
// and whether the conversation must be evicted first — the pre-sequence
// behaviour, used when there is not enough room or when image embeddings make
// the conversation's cell usage unknown.
func planIsolatedPrompt(nCtx, conversationTokens int, contaminated bool, promptTokens, requested int) (maxNew int, evict bool, err error) {
	if !contaminated {
		room := nCtx - conversationTokens
		need := requested
		if need <= 0 {
			need = minIsolatedReply
		}
		if room > 0 && promptTokens+need <= room {
			maxNew, err = generationBudget(promptTokens, requested, room)
			if err == nil {
				return maxNew, false, nil
			}
		}
	}
	maxNew, err = generationBudget(promptTokens, requested, nCtx)
	return maxNew, true, err
}

// prepareControlPrompt plans an isolated prompt and evicts the conversation
// only when the plan requires it. The control sequence is always empty here:
// releaseControlSequence clears it after every request.
func (r *Runtime) prepareControlPrompt(promptTokens, requested int) (int, error) {
	maxNew, evict, err := planIsolatedPrompt(r.nCtx, len(r.kvTokens), r.kvContaminated, promptTokens, requested)
	if err != nil {
		return 0, err
	}
	if evict {
		if err := r.kv.clear(r.mem, true); err != nil {
			return 0, fmt.Errorf("clear model memory before control prompt: %w", err)
		}
		r.kvTokens = []llama.Token{}
		r.kvContaminated = false
	}
	return maxNew, nil
}

// decodeControlPrompt evaluates an isolated prompt into the control sequence
// from position 0, in n_batch chunks.
func (r *Runtime) decodeControlPrompt(ctx context.Context, prompt []llama.Token, progress func(string)) error {
	for start := 0; start < len(prompt); start += r.batchSize {
		end := min(start+r.batchSize, len(prompt))
		emitProgress(progress, fmt.Sprintf("processing prompt %d/%d", end, len(prompt)))
		if err := r.decodeControl(ctx, prompt[start:end], llama.Pos(start)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) decodeControl(ctx context.Context, tokens []llama.Token, start llama.Pos) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code, err := r.kv.decodeBatch(r.lctx, controlSequence, tokens, start)
	if err != nil {
		return fmt.Errorf("decode control tokens: %w%s", err, nativeLogTail(3))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("decode control tokens: llama.cpp returned status %d%s", code, nativeLogTail(3))
	}
	return nil
}

// releaseControlSequence empties the control sequence so its cells return to
// the conversation. If llama.cpp refuses, all memory is cleared, which only
// costs the conversation prefix (the pre-sequence behaviour).
func (r *Runtime) releaseControlSequence() error {
	removed, err := r.kv.seqRm(r.mem, controlSequence, -1, -1)
	if err == nil && removed {
		return nil
	}
	cause := err
	if cause == nil {
		cause = errors.New("llama.cpp rejected control sequence removal")
	}
	if clearErr := r.kv.clear(r.mem, true); clearErr != nil {
		return errors.Join(
			fmt.Errorf("release control sequence: %w", cause),
			fmt.Errorf("clear model memory after release failure: %w", clearErr),
		)
	}
	r.kvTokens = []llama.Token{}
	r.kvContaminated = false
	return nil
}
