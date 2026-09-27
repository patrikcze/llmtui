package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestCallsFromNativeArgumentRecovery is the regression table for audit
// P2-11: the generic native decoder was strict on scalar spellings small
// models commonly produce and silent on undeclared keys, which could make a
// call succeed with a different meaning than the model intended.
func TestCallsFromNativeArgumentRecovery(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		args      string
		check     func(t *testing.T, c Call)
		wantErr   string
		wantNotes string
	}{
		{
			name: "numeric strings coerce for integer properties",
			tool: ToolReadFile, args: `{"path":"a.go","offset":"10","limit":" 200 "}`,
			check: func(t *testing.T, c Call) {
				if c.Offset != 10 || c.Limit != 200 || c.Path != "a.go" {
					t.Errorf("call = offset %d limit %d path %q", c.Offset, c.Limit, c.Path)
				}
			},
			wantNotes: "limit=integer,offset=integer",
		},
		{
			name: "integral float coerces",
			tool: ToolReadFile, args: `{"path":"a.go","offset":1,"limit":200.0}`,
			check: func(t *testing.T, c Call) {
				if c.Limit != 200 {
					t.Errorf("limit = %d", c.Limit)
				}
			},
			wantNotes: "limit=integer",
		},
		{
			name: "boolean strings coerce for boolean properties",
			tool: ToolGrep, args: `{"pattern":"TODO","literal":"true","case_sensitive":"False"}`,
			check: func(t *testing.T, c Call) {
				if !c.SearchLiteral || c.SearchCaseSensitive == nil || *c.SearchCaseSensitive {
					t.Errorf("literal=%v case=%v", c.SearchLiteral, c.SearchCaseSensitive)
				}
			},
			wantNotes: "case_sensitive=boolean,literal=boolean",
		},
		{
			name: "valid call is unchanged and carries no notes",
			tool: ToolListDir, args: `{"path":"docs","limit":20}`,
			check: func(t *testing.T, c Call) {
				if c.Path != "docs" || c.SearchLimit != 20 {
					t.Errorf("call = %+v", c)
				}
			},
		},
		{
			name: "undeclared key is rejected, not silently dropped",
			tool: ToolReadFile, args: `{"path":"a.go","start_line":400}`,
			wantErr: `unknown argument(s) "start_line"; read_file accepts only: byte_offset, limit, offset, path, resource_id`,
		},
		{
			name: "file_path is accepted as an alias for path",
			tool: ToolWriteFile, args: `{"file_path":"a.txt","content":"x"}`,
			wantNotes: "file_path=path",
			check: func(t *testing.T, c Call) {
				if c.Path != "a.txt" || c.Body != "x" {
					t.Errorf("call = %+v", c)
				}
			},
		},
		{
			name: "file_path alias still combines with coercion",
			tool: ToolReadFile, args: `{"file_path":"a.go","limit":"20"}`,
			wantNotes: "file_path=path,limit=integer",
			check: func(t *testing.T, c Call) {
				if c.Path != "a.go" {
					t.Errorf("call = %+v", c)
				}
			},
		},
		{
			name: "file_path and path together are rejected, not guessed",
			tool: ToolReadFile, args: `{"file_path":"a.go","path":"b.go"}`,
			wantErr: `both "file_path" and "path" given; read_file accepts only "path"`,
		},
		{
			name: "other misnamed keys are still rejected",
			tool: ToolWriteFile, args: `{"filename":"a.txt","content":"x"}`,
			wantErr: `unknown argument(s) "filename"; write_file accepts only: content, expected_resource_id, path`,
		},
		{
			name: "non-numeric string for an integer gets a schema message",
			tool: ToolReadFile, args: `{"path":"a.go","limit":"all"}`,
			wantErr: `"limit" must be an integer, got string`,
		},
		{
			name: "fractional number is not coerced",
			tool: ToolReadFile, args: `{"path":"a.go","limit":20.5}`,
			wantErr: `"limit" must be an integer, got number`,
		},
		{
			name: "non-object arguments are rejected",
			tool: ToolReadFile, args: `["a.go"]`,
			wantErr: "arguments must be a JSON object, got array",
		},
		{
			name: "empty arguments keep the tool's own validation",
			tool: ToolListDir, args: ``,
			check: func(t *testing.T, c Call) {
				if c.InputErr != "" || c.Path != "" {
					t.Errorf("call = %+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: tc.tool, Arguments: tc.args}})
			if len(calls) != 1 {
				t.Fatalf("calls = %d", len(calls))
			}
			c := calls[0]
			if tc.wantErr != "" {
				if !strings.Contains(c.InputErr, tc.wantErr) {
					t.Fatalf("InputErr = %q, want it to contain %q", c.InputErr, tc.wantErr)
				}
				return
			}
			if c.InputErr != "" {
				t.Fatalf("InputErr = %q, want none", c.InputErr)
			}
			if c.ArgumentNotes != tc.wantNotes {
				t.Errorf("ArgumentNotes = %q, want %q", c.ArgumentNotes, tc.wantNotes)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

// TestNativeDecoderAcceptsEveryDeclaredProperty guards against the decoder
// rejecting a key the model was told it may send: every property declared
// by a generic tool's schema must decode without an unknown-key error.
func TestNativeDecoderAcceptsEveryDeclaredProperty(t *testing.T) {
	generic := map[string]bool{
		ToolListDir: true, ToolReadFile: true, ToolGlob: true, ToolGrep: true, ToolWriteFile: true,
		ToolEditFile: true, ToolRunCommand: true, ToolWebSearch: true, ToolWebFetch: true, ToolSkillLoad: true,
	}
	seen := map[string]bool{}
	for tool, props := range nativePropertyTypes() {
		if !generic[tool] {
			continue
		}
		seen[tool] = true
		for name, kind := range props {
			value := map[string]string{"integer": `1`, "boolean": `true`, "string": `"x"`}[kind]
			if value == "" {
				t.Errorf("%s.%s has unsupported type %q", tool, name, kind)
				continue
			}
			_, _, err := decodeNativeArgs(tool, `{"`+name+`":`+value+`}`)
			if err != nil && strings.Contains(err.Error(), "unknown argument") {
				t.Errorf("%s rejected its declared property %q: %v", tool, name, err)
			}
		}
	}
	for tool := range generic {
		if !seen[tool] {
			t.Errorf("no schema found for generic tool %s", tool)
		}
	}
}

// TestArgumentNotesNeverContainValues keeps the diagnostic content-free.
func TestArgumentNotesNeverContainValues(t *testing.T) {
	_, notes, err := decodeNativeArgs(ToolReadFile, `{"path":"secret-token-dir/a.go","offset":"77"}`)
	if err != nil {
		t.Fatal(err)
	}
	joined, _ := json.Marshal(notes)
	if strings.Contains(string(joined), "77") || strings.Contains(string(joined), "secret") {
		t.Fatalf("notes %s leak argument values", joined)
	}
}

// TestFilePathAliasKeepsWorkspaceConfinement pins that the file_path alias
// only renames the key: an escaping path sent as file_path is refused by the
// runner exactly as it is when sent as path (Workspace Tool Safety
// Invariants).
func TestFilePathAliasKeepsWorkspaceConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("do not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, secret)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	for _, target := range []string{secret, rel} {
		for _, tool := range []string{ToolReadFile, ToolWriteFile} {
			args, _ := json.Marshal(map[string]string{"file_path": target, "content": "x"})
			if tool == ToolReadFile {
				args, _ = json.Marshal(map[string]string{"file_path": target})
			}
			calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: tool, Arguments: string(args)}})
			if len(calls) != 1 || calls[0].InputErr != "" {
				t.Fatalf("%s %q: decode = %+v", tool, target, calls)
			}
			res := r.Execute(calls[0])
			if res.Err == nil {
				t.Fatalf("%s via file_path=%q escaped the workspace: %q", tool, target, res.Output)
			}
		}
	}
	if data, _ := os.ReadFile(secret); string(data) != "do not read" {
		t.Fatalf("file outside the workspace was modified: %q", data)
	}
}
