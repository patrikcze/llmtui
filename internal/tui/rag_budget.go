package tui

import "github.com/patrikcze/llmtui/internal/rag"

// ragSourceTokenCap is the token budget for workspace-RAG source chunks in
// Active Context. rag.retrieval.max_context_tokens can only tighten the
// memory.retrieval.source_tokens tier cap, never raise it, so lowering the RAG
// setting is honored and a default install's prompt size does not change.
func ragSourceTokenCap(sourceTokens, ragMaxContextTokens int) int {
	if ragMaxContextTokens > 0 && ragMaxContextTokens < sourceTokens {
		return ragMaxContextTokens
	}
	return sourceTokens
}

// shedOptionalRetrieval removes the lowest-ranked workspace-RAG unit from
// base and reports whether it removed anything. Workspace retrieval is
// optional context: when it is what keeps a request over the model window it
// is shrunk before the request is rejected. It never touches memory tiers, and
// never mutates slices shared with the caller's copy. legacyMaxChars is the
// character cap for the non-Active-Context RetrievedContext block.
func shedOptionalRetrieval(base *compositionBase, legacyMaxChars int) bool {
	in := &base.input
	if in.UseActiveContext {
		for i := len(in.ActiveContext) - 1; i >= 0; i-- {
			if in.ActiveContext[i].Kind != "source_chunk" {
				continue
			}
			id := in.ActiveContext[i].ID
			in.ActiveContext = withoutIndex(in.ActiveContext, i)
			for j, hit := range base.memoryHits {
				if string(hit.Item.Kind) == "source_chunk" && hit.Item.ID == id {
					base.memoryHits = withoutIndex(base.memoryHits, j)
					break
				}
			}
			for j, r := range base.ragResults {
				if r.Chunk.ID == id {
					base.ragResults = withoutIndex(base.ragResults, j)
					break
				}
			}
			return true
		}
		return false
	}
	if len(base.ragResults) == 0 {
		return false
	}
	base.ragResults = base.ragResults[: len(base.ragResults)-1 : len(base.ragResults)-1]
	in.RetrievedContext = rag.FormatContext(base.ragResults, legacyMaxChars)
	return true
}

func withoutIndex[T any](s []T, i int) []T {
	out := make([]T, 0, len(s)-1)
	out = append(out, s[:i]...)
	return append(out, s[i+1:]...)
}
