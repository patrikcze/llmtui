package skill

import (
	"strings"
	"testing"
)

const validSkill = `---
schema_version: 1
id: go-agent-loop-review
name: Go Agent Loop Review
version: 1.0.0
description: Review a Go-based LLM agent loop, tool execution, and MCP integration.
tags:
  - go
  - agents
triggers:
  - review the agent loop
recommended_tools:
  - read_file
capabilities:
  tool_calling: optional
---

# Go Agent Loop Review

1. Trace the complete model and tool loop.
2. Verify message ordering.
`

func TestParseValidSkill(t *testing.T) {
	s, err := Parse([]byte(validSkill), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Meta.ID != "go-agent-loop-review" {
		t.Errorf("ID = %q", s.Meta.ID)
	}
	if s.Meta.Name != "Go Agent Loop Review" || s.Meta.Version != "1.0.0" {
		t.Errorf("meta = %+v", s.Meta)
	}
	if len(s.Meta.Tags) != 2 || s.Meta.Tags[0] != "go" {
		t.Errorf("tags = %v", s.Meta.Tags)
	}
	if s.Meta.Capabilities["tool_calling"] != "optional" {
		t.Errorf("capabilities = %v", s.Meta.Capabilities)
	}
	if !strings.HasPrefix(s.Body, "# Go Agent Loop Review") {
		t.Errorf("body = %q", s.Body)
	}
	if !strings.Contains(s.Body, "Verify message ordering.") {
		t.Errorf("body lost content: %q", s.Body)
	}
	if s.Size != len(validSkill) {
		t.Errorf("Size = %d, want %d", s.Size, len(validSkill))
	}
	if len(s.Hash) != 64 {
		t.Errorf("Hash = %q", s.Hash)
	}
}

func TestParseHashDeterministic(t *testing.T) {
	a, err := Parse([]byte(validSkill), 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(validSkill), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Errorf("hash not deterministic: %s vs %s", a.Hash, b.Hash)
	}
	c, err := Parse([]byte(validSkill+"\nextra"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Error("different content produced the same hash")
	}
}

func mustSkillDoc(id, name, desc, body string) string {
	return "---\nschema_version: 1\nid: " + id + "\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
}

func TestParseRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"empty", "", "empty"},
		{"no front matter", "# just markdown", "front matter"},
		{"unterminated front matter", "---\nid: x\n", "unterminated"},
		{"unknown schema version", "---\nschema_version: 99\nid: x\nname: X\ndescription: d\n---\nbody", "schema_version"},
		{"missing id", "---\nschema_version: 1\nname: X\ndescription: d\n---\nbody", "id is required"},
		{"invalid id uppercase", mustSkillDoc("Bad-ID", "X", "d", "body"), "invalid"},
		{"invalid id traversal", mustSkillDoc("a..b", "X", "d", "body"), "invalid"},
		{"invalid id slash", "---\nschema_version: 1\nid: a/b\nname: X\ndescription: d\n---\nbody", "invalid"},
		{"missing name", "---\nschema_version: 1\nid: x\ndescription: d\n---\nbody", "needs a name"},
		{"missing description", "---\nschema_version: 1\nid: x\nname: X\n---\nbody", "needs a description"},
		{"empty body", mustSkillDoc("x", "X", "d", "   \n"), "no instruction body"},
		{"bad yaml", "---\nschema_version: [\n---\nbody", "front matter"},
		{"unknown field", "---\nschema_version: 1\nid: x\nname: X\ndescription: d\nrun_command: rm -rf /\n---\nbody", "field run_command not found"},
		{"invalid utf8", "---\nschema_version: 1\nid: x\n\xff\xfe---\nbody", "UTF-8"},
		{"hidden control char", mustSkillDoc("x", "X", "d", "body\x1b[31mred"), "control character"},
		{"zero width", mustSkillDoc("x", "X", "d", "bo\u200bdy"), "control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc), 0)
			if err == nil {
				t.Fatalf("Parse accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseOversized(t *testing.T) {
	doc := mustSkillDoc("x", "X", "d", strings.Repeat("a", 2000))
	if _, err := Parse([]byte(doc), 1024); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized skill accepted (err = %v)", err)
	}
	if _, err := Parse([]byte(doc), 0); err != nil {
		t.Fatalf("default limit rejected a small skill: %v", err)
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{"a", "go-review", "jira.worklog", "x_1", "a1-b2.c3_d4"}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", "-a", ".a", "_a", "A", "a b", "a/b", "a\\b", "a..b", strings.Repeat("a", 65)}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) accepted", id)
		}
	}
}

func TestQualifiedID(t *testing.T) {
	s := Skill{Meta: Meta{ID: "worklog"}, Source: SourcePlugin, PluginID: "jira-tools"}
	if got := s.QualifiedID(); got != "plugin:jira-tools/worklog" {
		t.Errorf("QualifiedID = %q", got)
	}
	s = Skill{Meta: Meta{ID: "go-review"}, Source: SourceWorkspace}
	if got := s.QualifiedID(); got != "workspace:go-review" {
		t.Errorf("QualifiedID = %q", got)
	}
}
