package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// DefaultMaxSkillBytes caps one skill file when the caller passes no limit.
const DefaultMaxSkillBytes = 64 * 1024

// Parse parses and validates one SKILL.md document. maxBytes <= 0 uses
// DefaultMaxSkillBytes. The raw content is hashed as-is so the fingerprint is
// deterministic across platforms and re-reads.
func Parse(raw []byte, maxBytes int) (Skill, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSkillBytes
	}
	if len(raw) == 0 {
		return Skill{}, fmt.Errorf("skill file is empty")
	}
	if len(raw) > maxBytes {
		return Skill{}, fmt.Errorf("skill file is %d bytes, over the %d byte limit (skills.max_skill_kb)", len(raw), maxBytes)
	}
	if !utf8.Valid(raw) {
		return Skill{}, fmt.Errorf("skill file is not valid UTF-8")
	}
	text := string(raw)
	if err := checkControlChars(text); err != nil {
		return Skill{}, err
	}

	front, body, err := splitFrontMatter(text)
	if err != nil {
		return Skill{}, err
	}

	var meta Meta
	dec := yaml.NewDecoder(strings.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return Skill{}, fmt.Errorf("parse front matter: %w", err)
	}
	if err := validateMeta(meta); err != nil {
		return Skill{}, err
	}
	if strings.TrimSpace(body) == "" {
		return Skill{}, fmt.Errorf("skill %q has no instruction body", meta.ID)
	}

	sum := sha256.Sum256(raw)
	return Skill{
		Meta: meta,
		Body: strings.TrimSpace(body),
		Hash: hex.EncodeToString(sum[:]),
		Size: len(raw),
	}, nil
}

// splitFrontMatter extracts the YAML block between the leading "---" fence
// and the next "---" line. The front matter is mandatory: a skill without
// metadata cannot be identified or trusted.
func splitFrontMatter(text string) (front, body string, err error) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return "", "", fmt.Errorf("missing YAML front matter (file must start with ---)")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return "", "", fmt.Errorf("unterminated YAML front matter (no closing ---)")
}

// checkControlChars rejects hidden control and invisible formatting
// characters that could smuggle unseen instructions or corrupt terminal
// rendering. Tabs, newlines, and carriage returns are the only control
// characters allowed. Unicode category Cf covers zero-width and
// bidi-override characters (U+200B…U+200F, U+202A…U+202E, …).
func checkControlChars(text string) error {
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("skill file contains a hidden control character (U+%04X)", r)
		}
	}
	return nil
}

func validateMeta(meta Meta) error {
	if meta.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (this build understands %d)", meta.SchemaVersion, SchemaVersion)
	}
	if err := ValidateID(meta.ID); err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("skill %q needs a name", meta.ID)
	}
	if strings.TrimSpace(meta.Description) == "" {
		return fmt.Errorf("skill %q needs a description", meta.ID)
	}
	return nil
}
