// Package prompt builds provider-ready messages from composition sections.
//
// Design principle: the raw user message is never rewritten. Helpers
// (system prompt, template, model hints, session summary, memory) are
// separate, inspectable sections; the user's text goes to the provider
// verbatim as the final user message.
package prompt

import (
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Modes control how much helper context is added.
const (
	ModeMinimal  = "minimal"  // system prompt + conversation only
	ModeBalanced = "balanced" // all enabled helpers
	ModeCoding   = "coding"   // balanced + coding guidance
	ModeStrict   = "strict"   // system prompt + strict instruction, no extras
)

// ValidMode reports whether s is a known prompt mode.
func ValidMode(s string) bool {
	switch s {
	case ModeMinimal, ModeBalanced, ModeCoding, ModeStrict:
		return true
	}
	return false
}

// DefaultHelperText is the standard local-assistant guidance, configurable
// via prompt.helper_text and always visible in /prompt composed.
const DefaultHelperText = `You are running locally inside a terminal chat application.
Follow the user's request exactly.
Do not invent external tool results.
When unsure, say what is uncertain.
Prefer concise, practical answers unless the user asks for detail.
Preserve the user's intent. Do not transform the task into a different task.`

const codingHelperText = `For code tasks, provide runnable code and mention assumptions.
Prefer complete, working examples over fragments.`

// Include toggles individual helper sections.
type Include struct {
	SessionSummary  bool
	LocalMemory     bool
	ModelHints      bool
	FormattingHints bool
}

// Input carries everything the composer may use.
type Input struct {
	RawMessage     string
	Images         []provider.Image
	SystemPrompt   string
	TemplateName   string
	TemplatePrompt string
	Mode           string
	HelperText     string   // defaults to DefaultHelperText
	ModelHints     []string // from the model profile
	SessionSummary string
	MemorySnippets []string
	RecentMessages []provider.Message // prior turns, without system prompt
	Include        Include
}

// Section is one labeled part of the composition, for /prompt preview.
type Section struct {
	Title   string
	Content string
}

// Output is the composed request plus its inspectable sections.
type Output struct {
	Messages []provider.Message
	Sections []Section
}

// Compose builds provider messages. The system message concatenates enabled
// helper sections; conversation history follows; the raw user message is
// appended verbatim as the final user message.
func Compose(in Input) Output {
	if in.Mode == "" {
		in.Mode = ModeBalanced
	}
	helper := in.HelperText
	if helper == "" {
		helper = DefaultHelperText
	}

	var sections []Section
	add := func(title, content string) {
		if strings.TrimSpace(content) != "" {
			sections = append(sections, Section{Title: title, Content: strings.TrimSpace(content)})
		}
	}

	add("System Prompt", in.SystemPrompt)

	switch in.Mode {
	case ModeMinimal:
		// no helpers
	case ModeStrict:
		add("Strict Instruction", "Answer the user's request exactly as stated. Add nothing beyond what was asked.")
	default: // balanced, coding
		if in.TemplatePrompt != "" {
			title := "Template Prompt"
			if in.TemplateName != "" {
				title += " (" + in.TemplateName + ")"
			}
			add(title, in.TemplatePrompt)
		}
		if in.Include.FormattingHints {
			add("Helper Instructions", helper)
		}
		if in.Mode == ModeCoding {
			add("Coding Guidance", codingHelperText)
		}
		if in.Include.ModelHints && len(in.ModelHints) > 0 {
			add("Model Helper Hints", strings.Join(in.ModelHints, "\n"))
		}
		if in.Include.SessionSummary && in.SessionSummary != "" {
			add("Session Summary", "Summary of earlier conversation (not verbatim):\n"+in.SessionSummary)
		}
		if in.Include.LocalMemory && len(in.MemorySnippets) > 0 {
			add("Relevant Memory", "User preferences from local memory:\n- "+strings.Join(in.MemorySnippets, "\n- "))
		}
	}

	var system strings.Builder
	for i, s := range sections {
		if i > 0 {
			system.WriteString("\n\n")
		}
		system.WriteString(s.Content)
	}

	var msgs []provider.Message
	if system.Len() > 0 {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: system.String()})
	}
	msgs = append(msgs, in.RecentMessages...)

	// The raw user message: verbatim, always last.
	msgs = append(msgs, provider.Message{
		Role:    provider.RoleUser,
		Content: in.RawMessage,
		Images:  in.Images,
	})

	// Preview-only sections for recent conversation and the raw message.
	preview := make([]Section, len(sections), len(sections)+2)
	copy(preview, sections)
	if n := len(in.RecentMessages); n > 0 {
		preview = append(preview, Section{
			Title:   "Recent Messages",
			Content: summarizeRecent(in.RecentMessages),
		})
	}
	preview = append(preview, Section{Title: "Raw User Message", Content: in.RawMessage})

	return Output{Messages: msgs, Sections: preview}
}

func summarizeRecent(msgs []provider.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		content := m.Content
		if len(content) > 120 {
			content = content[:119] + "…"
		}
		b.WriteString(string(m.Role) + ": " + strings.ReplaceAll(content, "\n", " "))
	}
	return b.String()
}

// HintsForProfile derives helper hints from model profile attributes.
func HintsForProfile(style string, reasoningHint bool) []string {
	var hints []string
	switch style {
	case "coding_assistant":
		hints = append(hints, "This model is tuned for code; prefer concrete code answers.")
	case "direct":
		hints = append(hints, "Answer directly without excessive preamble.")
	}
	if reasoningHint {
		hints = append(hints, "Think through the problem before answering, but keep the visible answer concise.")
	}
	return hints
}
