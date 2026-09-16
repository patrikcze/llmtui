// Package redact holds the one shared, best-effort secret-shaped-text
// pattern used before writing a bounded record to disk (agent runs,
// project facts, episode summaries). It is not a secret manager and it is
// not exhaustive — see Secrets' doc comment — so callers must still avoid
// storing raw prompts, tool output, or credentials in the first place;
// this is a last-line pattern match, not a substitute for that discipline.
//
// A leaf package with no internal imports, so every caller (internal/agent,
// internal/history, internal/memoryindex, ...) can depend on it without
// creating an import cycle.
package redact

import "regexp"

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)((?:token|secret|password|passwd|authorization|api[_-]?key)\s*[=:]\s*)[^\s,;}]+`)
	bearerPattern           = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	keyPattern              = regexp.MustCompile(`\b(?:sk|ghp|github_pat)-[A-Za-z0-9_-]{8,}\b`)
	privateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----`)
)

// Secrets replaces likely private-key blocks, bearer tokens, key=value/
// key:value credential assignments, and sk-/ghp-/github_pat- style API
// keys with a fixed placeholder. It is a best-effort pattern match, not a
// secret scanner: it catches the shapes llmtui's own persisted records
// have been observed to carry, nothing more.
func Secrets(value string) string {
	value = privateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretAssignmentPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return keyPattern.ReplaceAllString(value, "[REDACTED KEY]")
}
