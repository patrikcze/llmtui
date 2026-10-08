package agent

import (
	"strings"
	"unicode"
)

// This file is the Phase 2 completion-integrity layer: it stops the
// controller from treating "every pinned criterion is resolved" as
// equivalent to "the request is fully covered" when the pinning itself may
// have missed part of the request. See finding #3 in
// .claude/tasks/plans/llmtui-agent-evolution.md §4 and the contract-coverage
// requirement in §11.6.

// mutationVerbs are action words that name a workspace-mutating step no
// read-only or purely informational criterion can satisfy. This is a
// narrow, conservative signal for one specific, previously observed
// contract-coverage gap — a contract naming only an atomic read while the
// request also asks for a write (e.g. "read report.md and write its heading
// to result.txt") — not a general natural-language task-completion
// judgment. It deliberately does not flag ordinary conjunctions like "and
// give me its heading", which name no distinct mutating step.
var mutationVerbs = []string{"write", "save", "create", "append", "update", "modify", "delete", "commit", "run", "execute", "send"}

// ContractCoverageJustified reports whether it is safe to treat a fully
// mechanically-resolved criteria set as proof the whole request was
// addressed, without a semantic verification pass. It is not when the
// contract pinned only one semantic criterion and the request's own text
// names a mutating action that criterion is not positioned to prove — see
// requestNamesUnaddressedMutation. Genuine decomposition (more than one
// pinned criterion) is always trusted: the contract already broke the
// request apart, so there is no coverage gap this package can detect that
// the contract itself did not already consider.
func (r *AgentRun) ContractCoverageJustified() bool {
	if r == nil {
		return true
	}
	return !requestNamesUnaddressedMutation(r.Request, r.Criteria)
}

// requestNamesUnaddressedMutation reports whether request names a
// mutation-shaped action that the sole pinned criterion is not positioned to
// prove. A single criterion came from the contract's own free-form
// decomposition of a request that may have had more than one part, so a
// mutation verb in the request means the verifier must still look.
func requestNamesUnaddressedMutation(request string, criteria []Criterion) bool {
	if len(criteria) != 1 {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(request), func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' })
	for i, word := range words {
		for _, verb := range mutationVerbs {
			if wordNamesVerb(word, verb) && !directlyNegated(words, i) {
				return true
			}
		}
	}
	return false
}

// directlyNegated recognizes only an immediate, explicit prohibition. It is
// not a general intent parser: another affirmative action in the request
// still requires its normal receipt, even after a negative clause.
func directlyNegated(words []string, index int) bool {
	if index == 0 {
		return false
	}
	switch words[index-1] {
	case "not", "never", "without", "don't", "dont":
		return true
	default:
		return false
	}
}

// wordNamesVerb reports whether word is a form of verb. Matching is per
// word rather than per substring so "return" or "prune" no longer read as
// "run" (audit P3-5), while staying deliberately loose in the safe
// direction: inflections ("writes", "created", "running"), the e-dropping
// "-ing" form ("writing", "saving"), and prefixed forms ("overwrite",
// "rerun", "resend") all still count. A false positive only costs one
// semantic verification pass.
func wordNamesVerb(word, verb string) bool {
	if strings.HasPrefix(word, verb) || strings.HasSuffix(word, verb) {
		return true
	}
	if stem, ok := strings.CutSuffix(verb, "e"); ok && strings.HasPrefix(word, stem+"ing") {
		return true
	}
	return false
}

// MissingFileWriteReceipt reports a narrow, observable gap: the user asked
// for a workspace file to be written, but no successful write_file/edit_file
// call was recorded. Verifier modes that skip semantic review must not turn
// the executor's prose claim into a completed file mutation.
func MissingFileWriteReceipt(request string, execution ExecutionResult) bool {
	lower := strings.ToLower(strings.TrimSpace(request))
	if strings.HasPrefix(lower, "explain how to ") || strings.HasPrefix(lower, "how do i ") {
		return false
	}
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' && r != '\''
	})
	wantsWrite, namesFile := false, false
	for i, word := range words {
		for _, verb := range []string{"write", "save", "create", "append", "update", "modify"} {
			if wordNamesVerb(word, verb) && !directlyNegated(words, i) {
				wantsWrite = true
			}
		}
		if word == "file" || word == "files" || word == "document" || word == "report" ||
			strings.HasSuffix(word, ".md") || strings.HasSuffix(word, ".txt") || strings.HasSuffix(word, ".json") {
			namesFile = true
		}
	}
	if !wantsWrite || !namesFile {
		return false
	}
	return !HasFileWriteReceipt(execution)
}

// HasFileWriteReceipt reads controller-owned tool results, never assistant
// prose. A successful whole-file or surgical write is a valid receipt even
// when the file already held the requested bytes and no diff was produced.
func HasFileWriteReceipt(execution ExecutionResult) bool {
	for _, call := range execution.ToolCalls {
		if call.Succeeded && (call.Name == "write_file" || call.Name == "edit_file") {
			return true
		}
	}
	return false
}
