// Package modelprofile tunes defaults per local model family: context
// window, temperature, prompt style, and capability hints. Profiles live in
// config; built-ins cover common families.
package modelprofile

import "strings"

// Profile describes how to treat one family of models.
type Profile struct {
	Name                 string
	Match                []string // lowercase substrings of model IDs
	ContextWindow        int
	PreferredTemperature float64
	SupportsJSONMode     bool
	PromptStyle          string // direct | plain | coding_assistant
	ReasoningHint        bool
}

// Default is the fallback profile when nothing matches.
func Default() Profile {
	return Profile{
		Name:                 "default",
		ContextWindow:        8192,
		PreferredTemperature: 0.7,
		PromptStyle:          "plain",
	}
}

// BuiltIn returns the built-in profiles, checked in order. Config-defined
// profiles are matched before these.
func BuiltIn() []Profile {
	return []Profile{
		{
			Name:                 "coder",
			Match:                []string{"coder", "codellama", "deepseek", "starcoder", "codegemma", "codestral"},
			ContextWindow:        32768,
			PreferredTemperature: 0.25,
			SupportsJSONMode:     true,
			PromptStyle:          "coding_assistant",
			ReasoningHint:        true,
		},
		{
			Name:                 "qwen",
			Match:                []string{"qwen", "qwythos"},
			ContextWindow:        32768,
			PreferredTemperature: 0.6,
			SupportsJSONMode:     true,
			PromptStyle:          "direct",
			ReasoningHint:        true,
		},
		{
			// gpt-oss-20b/120b: OpenAI's open-weight reasoning+tool-calling
			// models, 131072-token native context.
			Name:                 "gpt-oss",
			Match:                []string{"gpt-oss"},
			ContextWindow:        131072,
			PreferredTemperature: 0.6,
			SupportsJSONMode:     true,
			PromptStyle:          "coding_assistant",
			ReasoningHint:        true,
		},
		{
			Name:                 "llama",
			Match:                []string{"llama", "mistral", "mixtral"},
			ContextWindow:        8192,
			PreferredTemperature: 0.7,
			PromptStyle:          "plain",
		},
		{
			Name:                 "gemma",
			Match:                []string{"gemma"},
			ContextWindow:        8192,
			PreferredTemperature: 0.7,
			PromptStyle:          "plain",
		},
	}
}

// Match finds the first profile whose patterns match the model ID as a
// whole segment, not an arbitrary substring. Profiles are checked in slice
// order, so callers should place config-defined profiles before built-ins.
func Match(profiles []Profile, modelID string) (Profile, bool) {
	id := strings.ToLower(modelID)
	for _, p := range profiles {
		for _, pat := range p.Match {
			if pat != "" && matchesSegment(id, strings.ToLower(pat)) {
				return p, true
			}
		}
	}
	return Default(), false
}

// matchesSegment reports whether pat occurs in id with a letter not
// directly attached on either side. Plain strings.Contains would let
// "coder" match "encoder" or "decoder" (both literally contain "coder" as a
// substring of a longer word), silently assigning the coding-assistant
// profile — 32768-token window, low temperature, coding prompt style — to
// unrelated models. Digits are deliberately allowed to sit directly against
// the pattern: model IDs routinely attach a version number with no
// separator ("qwen3", "llama3.1", "gemma3"), and that must keep matching.
func matchesSegment(id, pat string) bool {
	idx := 0
	for {
		i := strings.Index(id[idx:], pat)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(pat)
		beforeOK := start == 0 || !isLetter(id[start-1])
		afterOK := end == len(id) || !isLetter(id[end])
		if beforeOK && afterOK {
			return true
		}
		idx = start + 1
	}
}

func isLetter(b byte) bool {
	return b >= 'a' && b <= 'z'
}

// ByName finds a profile by exact name, for `/profile set <name>`.
func ByName(profiles []Profile, name string) (Profile, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	if name == "default" {
		return Default(), true
	}
	return Profile{}, false
}
