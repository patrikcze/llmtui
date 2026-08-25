// Package history persists chat sessions and a cumulative usage log under
// the configured history directory (chat.history_dir).
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/skill"
)

const (
	episodeVersion         = 1
	maxEpisodeGoalBytes    = 1024
	maxEpisodeOutcomeBytes = 2048
)

var (
	episodeSecretAssignmentPattern = regexp.MustCompile(
		`(?i)((?:token|secret|password|passwd|authorization|api[_-]?key)\s*[=:]\s*)[^\s,;}]+`,
	)
	episodeBearerPattern     = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	episodeKeyPattern        = regexp.MustCompile(`\b(?:sk|ghp|github_pat)-[A-Za-z0-9_-]{8,}\b`)
	episodePrivateKeyPattern = regexp.MustCompile(
		`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----`,
	)
)

// Episode is a compact, retrieval-safe record derived from visible session
// content. It never contains message history, images, reasoning, or tool-call
// arguments.
type Episode struct {
	Version    int       `json:"version"`
	Goal       string    `json:"goal,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Status     string    `json:"status"`
	Artifacts  []string  `json:"artifacts,omitempty"`
	Checks     []string  `json:"checks,omitempty"`
	Unresolved []string  `json:"unresolved,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Model      string    `json:"model,omitempty"`
	ProjectID  string    `json:"project_id,omitempty"`
	SavedAt    time.Time `json:"saved_at"`
}

// Session is the on-disk representation of one saved conversation.
// Image attachments are intentionally not persisted.
type Session struct {
	Version    int                `json:"version"`
	SavedAt    time.Time          `json:"saved_at"`
	Provider   string             `json:"provider"`
	Model      string             `json:"model"`
	Template   string             `json:"template,omitempty"`
	PromptMode string             `json:"prompt_mode,omitempty"`
	Profile    string             `json:"profile,omitempty"`
	ProjectID  string             `json:"project_id,omitempty"`
	Messages   []provider.Message `json:"messages"`
	Prompt     int                `json:"prompt_tokens"`
	Reply      int                `json:"completion_tokens"`
	Estimated  bool               `json:"estimated"`
	Episode    *Episode           `json:"episode,omitempty"`
	// Skills are the session-scoped skill activations at save time (run-scoped
	// activations are never persisted). On load they are re-resolved against
	// the current registry; a missing or changed skill produces a warning
	// instead of a silent substitution.
	Skills []skill.Ref `json:"skills,omitempty"`
}

// BuildEpisode deterministically summarizes the visible boundary of a saved
// session. It intentionally ignores tool calls, tool results, images, display
// annotations, and reasoning. Empty sessions do not produce an episode.
func BuildEpisode(s Session) *Episode {
	var goal string
	for _, message := range s.Messages {
		if message.Role == provider.RoleUser && strings.TrimSpace(message.Content) != "" {
			goal = episodeText(message.Content, maxEpisodeGoalBytes)
			break
		}
	}

	var outcome string
	for index := len(s.Messages) - 1; index >= 0; index-- {
		message := s.Messages[index]
		if message.Role == provider.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			outcome = episodeText(message.Content, maxEpisodeOutcomeBytes)
			break
		}
	}
	if goal == "" && outcome == "" {
		return nil
	}

	status := "in_progress"
	if outcome != "" {
		status = "response_recorded"
	}
	return &Episode{
		Version:   episodeVersion,
		Goal:      goal,
		Outcome:   outcome,
		Status:    status,
		Provider:  episodeText(s.Provider, 256),
		Model:     episodeText(s.Model, 256),
		ProjectID: s.ProjectID,
	}
}

func episodeText(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(redactEpisodeSecrets(value)), " ")
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func redactEpisodeSecrets(value string) string {
	value = episodePrivateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = episodeBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = episodeSecretAssignmentPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return episodeKeyPattern.ReplaceAllString(value, "[REDACTED KEY]")
}

// Meta summarizes a saved session for listings.
type Meta struct {
	Name     string
	SavedAt  time.Time
	Provider string
	Model    string
	Messages int
	Tokens   int
}

// ExpandHome resolves a leading "~/" against the user's home directory.
func ExpandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	return path, nil
}

// NewSessionName returns a unique, sortable session file name.
func NewSessionName(t time.Time) string {
	return "session-" + t.Format("20060102-150405")
}

// validName rejects session names that could escape the history directory
// (path separators, "..", hidden files) — names come from user input.
func validName(name string) error {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid session name %q", name)
	}
	return nil
}

// Save writes the session as <name>.json in dir, creating dir as needed.
// Saving the same name again overwrites, so a running chat updates in place.
func Save(dir, name string, s Session) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create history directory: %w", err)
	}
	s.Version = 1
	s.SavedAt = time.Now().UTC()
	if s.Episode != nil {
		episode := *s.Episode
		episode.Version = episodeVersion
		episode.SavedAt = s.SavedAt
		episode.Provider = episodeText(s.Provider, 256)
		episode.Model = episodeText(s.Model, 256)
		episode.ProjectID = s.ProjectID
		s.Episode = &episode
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}
	path := filepath.Join(dir, name+".json")
	// Write to a temp file and rename into place so a crash mid-write can
	// never leave a truncated or corrupted session at path — os.WriteFile
	// truncates the destination before writing, which loses the previous
	// save if the process dies partway through.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write session: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("finalize session: %w", err)
	}
	return path, nil
}

// Load reads a saved session by name.
func Load(dir, name string) (Session, error) {
	var s Session
	if err := validName(name); err != nil {
		return s, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return s, fmt.Errorf("read session: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse session: %w", err)
	}
	return s, nil
}

// List returns metadata for all saved sessions, newest first.
func List(dir string) ([]Meta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read history directory: %w", err)
	}

	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(dir, name)
		if err != nil {
			continue // skip unreadable/foreign files
		}
		metas = append(metas, Meta{
			Name:     name,
			SavedAt:  s.SavedAt,
			Provider: s.Provider,
			Model:    s.Model,
			Messages: len(s.Messages),
			Tokens:   s.Prompt + s.Reply,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].SavedAt.After(metas[j].SavedAt) })
	return metas, nil
}

// Latest returns the most recently saved session (by SavedAt) and the name
// it was saved under, for --continue. Returns an error if dir has no saved
// sessions.
func Latest(dir string) (name string, s Session, err error) {
	metas, err := List(dir)
	if err != nil {
		return "", Session{}, err
	}
	if len(metas) == 0 {
		return "", Session{}, fmt.Errorf("no saved sessions in %s", dir)
	}
	name = metas[0].Name
	s, err = Load(dir, name)
	return name, s, err
}
