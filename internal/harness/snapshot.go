package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/redact"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

const SchemaVersion = 1
const maxEdits = 3

// Edit selects application-owned text, never executable or model-authored text.
type Edit string

const (
	ExecuteTools      Edit = "execute_tools"
	VerifyArtifact    Edit = "verify_artifact"
	RecoverTyped      Edit = "recover_typed"
	CompleteRead      Edit = "complete_read"
	ArtifactProcedure Edit = "artifact_procedure"
	ReadProcedure     Edit = "read_procedure"
	FixtureRecovery   Edit = "fixture_recovery"
)

type Guidance struct {
	Component string `json:"component"`
	Text      string `json:"text"`
	Scope     string `json:"scope"`
}

// Catalog returns a fresh copy so callers cannot change the trusted vocabulary.
func Catalog() map[Edit]Guidance {
	return map[Edit]Guidance{
		ExecuteTools:      {"tool_guidance", "When the task requires an action, request the available tool through its supported protocol. Describing a call is not executing it. Existing approvals still apply.", "invariant"},
		VerifyArtifact:    {"procedure", "Before claiming completion, verify required artifacts and acceptance criteria using actual receipts. A success claim alone is not evidence.", "invariant"},
		RecoverTyped:      {"procedure", "After a recoverable typed tool error, correct the cause once using the receipt. Do not repeat the identical unsuccessful request; stop when the correction cannot be justified.", "invariant"},
		CompleteRead:      {"context_hints", "A partial read is not a full inspection. Use current versioned read receipts and the first missing interval, respecting existing read limits. Never infer unseen content.", "invariant"},
		ArtifactProcedure: {"procedure", "For fixture artifact tasks, inspect the input, produce the requested result file through an available tool, then check its contents against the task's explicit requirement.", "domain"},
		FixtureRecovery:   {"procedure", "For fixture input-recovery tasks, missing.txt is the failed target and input.txt is the available source. Confirm the current fixture identity before correcting the path once.", "domain"},
		ReadProcedure:     {"context_hints", "For fixture file-analysis tasks, inspect each required line window from the same source version and cite only delivered content.", "domain"},
	}
}

type Compatibility struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Runtime  string `json:"runtime"`
}

// Snapshot is a content-addressed overlay; the user's configuration is inherited.
type Snapshot struct {
	Schema        int           `json:"schema"`
	ID            string        `json:"id"`
	Fingerprint   string        `json:"fingerprint"`
	Parent        string        `json:"parent,omitempty"`
	Edits         []Edit        `json:"edits"`
	Components    []string      `json:"components"`
	Source        string        `json:"source"`
	Compatibility Compatibility `json:"compatibility"`
	Created       time.Time     `json:"created"`
	Validation    string        `json:"validation"`
	Rollback      string        `json:"rollback,omitempty"`
}

func NewSnapshot(parent string, edits []Edit, source string, compat Compatibility, now time.Time) (Snapshot, error) {
	edits = slices.Clone(edits)
	slices.Sort(edits)
	s := Snapshot{Schema: SchemaVersion, Parent: parent, Edits: edits, Source: source, Compatibility: compat, Created: now.UTC(), Validation: "catalog_validated", Rollback: parent}
	for _, edit := range edits {
		g, ok := Catalog()[edit]
		if !ok {
			return Snapshot{}, errors.New("unsupported or protected harness edit")
		}
		if !slices.Contains(s.Components, g.Component) {
			s.Components = append(s.Components, g.Component)
		}
	}
	s.Fingerprint = s.digest()
	s.ID = s.Fingerprint[:24]
	return s, s.Validate()
}

func (s Snapshot) digest() string {
	// Time and provenance are metadata, not part of content identity.
	b, _ := json.Marshal(struct {
		Schema        int
		Parent        string
		Edits         []Edit
		Compatibility Compatibility
		Catalog       map[Edit]Guidance
	}{s.Schema, s.Parent, s.Edits, s.Compatibility, Catalog()})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s Snapshot) Validate() error {
	if s.Schema != SchemaVersion {
		return errors.New("unsupported harness schema; no implicit migration")
	}
	if len(s.Edits) > maxEdits || s.Validation != "catalog_validated" || s.Created.IsZero() {
		return errors.New("invalid harness metadata")
	}
	if s.Source != "fixture" && s.Source != "provider" && s.Source != "composition" {
		return errors.New("invalid harness provenance")
	}
	if s.Compatibility.Provider == "" || s.Compatibility.Model == "" || len(s.Compatibility.Provider) > 64 || len(s.Compatibility.Model) > 512 || len(s.Compatibility.Runtime) > 128 {
		return errors.New("invalid harness compatibility")
	}
	for _, value := range []string{s.Compatibility.Provider, s.Compatibility.Model, s.Compatibility.Runtime} {
		if value != redact.Secrets(value) || value != terminaltext.Sanitize(value) {
			return errors.New("unsafe harness compatibility metadata")
		}
	}
	if s.Parent != s.Rollback || (s.Parent != "" && !validID(s.Parent)) {
		return errors.New("invalid rollback reference")
	}
	components := []string(nil)
	for i, edit := range s.Edits {
		g, ok := Catalog()[edit]
		if !ok || (i > 0 && edit <= s.Edits[i-1]) {
			return errors.New("unsupported, duplicate or unordered harness edit")
		}
		if !slices.Contains(components, g.Component) {
			components = append(components, g.Component)
		}
	}
	if !slices.Equal(components, s.Components) || s.Fingerprint != s.digest() || s.ID != s.Fingerprint[:24] {
		return errors.New("harness integrity check failed")
	}
	return nil
}

func validID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// GuidanceText renders only catalog text after validating the entire snapshot.
func (s Snapshot) GuidanceText() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, edit := range s.Edits {
		fmt.Fprintf(&b, "[%s] %s\n", edit, Catalog()[edit].Text)
	}
	return b.String(), nil
}

// Diff exposes component-level additions without rendering proposer prose.
func (s Snapshot) Diff(base Snapshot) []Edit {
	var out []Edit
	for _, e := range s.Edits {
		if !slices.Contains(base.Edits, e) {
			out = append(out, e)
		}
	}
	return out
}
