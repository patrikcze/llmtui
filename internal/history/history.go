// Package history persists chat sessions and a cumulative usage log under
// the configured history directory (chat.history_dir).
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

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
	Messages   []provider.Message `json:"messages"`
	Prompt     int                `json:"prompt_tokens"`
	Reply      int                `json:"completion_tokens"`
	Estimated  bool               `json:"estimated"`
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
	s.SavedAt = time.Now()
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
