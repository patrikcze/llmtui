package rag

import "regexp"

// contentSecretPatterns intentionally favors high-confidence credential
// shapes. RAG skips the whole file on a match: partial redaction can retain
// related key material and makes it harder to explain what was indexed.
// Values are never included in errors or logs.
var contentSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`),
	regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+[A-Za-z0-9._~+/=-]{20,}`),
	regexp.MustCompile(`(?i)\bx-api-key\s*:\s*[A-Za-z0-9._~+/=-]{20,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
}

func containsLikelySecret(data []byte) bool {
	for _, pattern := range contentSecretPatterns {
		if pattern.FindIndex(data) != nil {
			return true
		}
	}
	return false
}
