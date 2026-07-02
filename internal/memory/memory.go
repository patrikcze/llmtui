// Package memory stores small, user-curated preference snippets locally.
// It is disabled by default, never auto-stores anything, and must never
// hold secrets.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Snippet is one remembered preference.
type Snippet struct {
	ID        string    `yaml:"id"`
	Text      string    `yaml:"text"`
	CreatedAt time.Time `yaml:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at"`
	Tags      []string  `yaml:"tags,omitempty"`
}

// Store persists snippets to one YAML file.
type Store struct {
	path string
	max  int
}

// NewStore creates a store writing to path, keeping at most max snippets.
func NewStore(path string, max int) *Store {
	if max <= 0 {
		max = 100
	}
	return &Store{path: path, max: max}
}

// Load reads all snippets; a missing file is an empty store.
func (s *Store) Load() ([]Snippet, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory file: %w", err)
	}
	var snippets []Snippet
	if err := yaml.Unmarshal(data, &snippets); err != nil {
		return nil, fmt.Errorf("parse memory file: %w", err)
	}
	return snippets, nil
}

func (s *Store) save(snippets []Snippet) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	data, err := yaml.Marshal(snippets)
	if err != nil {
		return fmt.Errorf("encode memory: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}
	return nil
}

// Add stores a new snippet, evicting the oldest when over the limit.
func (s *Store) Add(text string, tags ...string) (Snippet, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Snippet{}, fmt.Errorf("memory text is empty")
	}
	snippets, err := s.Load()
	if err != nil {
		return Snippet{}, err
	}
	now := time.Now()
	sn := Snippet{ID: newID(), Text: text, CreatedAt: now, UpdatedAt: now, Tags: tags}
	snippets = append(snippets, sn)
	if len(snippets) > s.max {
		sort.Slice(snippets, func(i, j int) bool { return snippets[i].CreatedAt.Before(snippets[j].CreatedAt) })
		snippets = snippets[len(snippets)-s.max:]
	}
	return sn, s.save(snippets)
}

// Remove deletes a snippet by ID (or unambiguous ID prefix).
func (s *Store) Remove(id string) error {
	snippets, err := s.Load()
	if err != nil {
		return err
	}
	var out []Snippet
	matched := 0
	for _, sn := range snippets {
		if sn.ID == id || (len(id) >= 4 && strings.HasPrefix(sn.ID, id)) {
			matched++
			continue
		}
		out = append(out, sn)
	}
	if matched == 0 {
		return fmt.Errorf("no memory snippet with id %q", id)
	}
	return s.save(out)
}

// Clear removes all snippets.
func (s *Store) Clear() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear memory: %w", err)
	}
	return nil
}

// Relevant returns up to limit snippets whose words overlap the query,
// best matches first. Simple keyword matching keeps this fully local.
func Relevant(snippets []Snippet, query string, limit int) []Snippet {
	if limit <= 0 || len(snippets) == 0 {
		return nil
	}
	queryWords := keywordSet(query)
	type scored struct {
		s     Snippet
		score int
	}
	var matches []scored
	for _, sn := range snippets {
		score := 0
		for w := range keywordSet(sn.Text) {
			if queryWords[w] {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, scored{sn, score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Snippet, len(matches))
	for i, m := range matches {
		out[i] = m.s
	}
	return out
}

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "to": true,
	"of": true, "in": true, "for": true, "is": true, "are": true, "i": true,
	"me": true, "my": true, "you": true, "it": true, "with": true, "use": true,
	"using": true, "please": true, "prefer": true, "when": true, "how": true,
	"what": true, "can": true, "do": true, "does": true,
}

func keywordSet(text string) map[string]bool {
	words := map[string]bool{}
	isWordRune := func(r rune) bool {
		return 'a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '.' || r == '-'
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !isWordRune(r)
	}) {
		if len(w) >= 2 && !stopwords[w] {
			words[w] = true
		}
	}
	return words
}

func newID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
