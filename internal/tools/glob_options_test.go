package tools

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestGlobExcludeAndCaseOptions(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "src/Config.go", "package src\n")
	writeSearchFixture(t, root, "src/config_test.go", "package src\n")
	writeSearchFixture(t, root, "node_modules/pkg/config.js", "x\n")
	writeSearchFixture(t, root, "docs/configuration.md", "x\n")
	writeSearchFixture(t, root, "dist/app.min.js", "x\n")
	r := NewRunner(root, 64)
	no := false

	tests := []struct {
		name      string
		call      Call
		want      []string
		unwanted  []string
		wantError bool
	}{
		{
			name:     "case sensitive by default",
			call:     Call{Tool: ToolGlob, Body: "*config*"},
			want:     []string{"src/config_test.go", "node_modules/pkg/config.js", "docs/configuration.md"},
			unwanted: []string{"src/Config.go"},
		},
		{
			name: "case insensitive partial name match",
			call: Call{Tool: ToolGlob, Body: "*CONFIG*", SearchCaseSensitive: &no},
			want: []string{"src/Config.go", "src/config_test.go", "docs/configuration.md"},
		},
		{
			name:     "bare exclude prunes a directory at any depth",
			call:     Call{Tool: ToolGlob, Body: "*config*", Filter: "node_modules"},
			want:     []string{"src/config_test.go"},
			unwanted: []string{"node_modules/pkg/config.js"},
		},
		{
			name:     "path exclude skips matching files",
			call:     Call{Tool: ToolGlob, Body: "**/*.js", Filter: "**/*.min.js"},
			want:     []string{"node_modules/pkg/config.js"},
			unwanted: []string{"dist/app.min.js"},
		},
		{
			name:     "exclude follows the case policy",
			call:     Call{Tool: ToolGlob, Body: "*.go", Filter: "*CONFIG*", SearchCaseSensitive: &no},
			unwanted: []string{"src/Config.go", "src/config_test.go"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := r.Execute(tc.call)
			if res.Err != nil {
				t.Fatalf("glob: %v", res.Err)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.Output, want) {
					t.Errorf("missing %q in %q", want, res.Output)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(res.Output, unwanted) {
					t.Errorf("unexpected %q in %q", unwanted, res.Output)
				}
			}
		})
	}
}

func TestGlobExcludeDecoding(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		calls := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolGlob, Arguments: `{"pattern":"*x*","exclude":["vendor"," ","**/*.gen.go"],"case_sensitive":false}`}})
		c := calls[0]
		if c.InputErr != "" {
			t.Fatalf("input error: %s", c.InputErr)
		}
		if c.Filter != "vendor\n**/*.gen.go" || c.SearchCaseSensitive == nil || *c.SearchCaseSensitive {
			t.Fatalf("decoded filter=%q case=%v", c.Filter, c.SearchCaseSensitive)
		}
	})
	t.Run("native rejects parent segments in excludes", func(t *testing.T) {
		calls := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolGlob, Arguments: `{"pattern":"*","exclude":["../x"]}`}})
		if calls[0].InputErr == "" {
			t.Fatal("expected input error for ../ exclude")
		}
	})
	t.Run("native rejects too many excludes", func(t *testing.T) {
		many := strings.Repeat(`"a",`, MaxGlobExcludes) + `"a"`
		calls := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolGlob, Arguments: `{"pattern":"*","exclude":[` + many + `]}`}})
		if calls[0].InputErr == "" {
			t.Fatal("expected input error for too many excludes")
		}
	})
	t.Run("fenced", func(t *testing.T) {
		calls := Parse("```tool glob src\n{\"pattern\":\"*.go\",\"exclude\":[\"*_test.go\"],\"case_sensitive\":false}\n```\n")
		if len(calls) != 1 || calls[0].InputErr != "" || calls[0].Body != "*.go" || calls[0].Filter != "*_test.go" {
			t.Fatalf("fenced decode: %+v", calls)
		}
	})
	t.Run("list_dir keeps rejecting glob-only keys as a legacy body", func(t *testing.T) {
		calls := Parse("```tool list_dir\n{\"exclude\":[\"x\"]}\n```\n")
		if len(calls) != 1 || calls[0].Filter != "" {
			t.Fatalf("list_dir decode changed: %+v", calls)
		}
	})
}

func TestGlobCursorIdentityCoversExcludes(t *testing.T) {
	a := listingHash(Call{Tool: ToolGlob, Body: "*"})
	b := listingHash(Call{Tool: ToolGlob, Body: "*", Filter: "vendor"})
	no := false
	c := listingHash(Call{Tool: ToolGlob, Body: "*", SearchCaseSensitive: &no})
	if a == b || a == c || b == c {
		t.Fatal("glob cursor identity must change with excludes and case policy")
	}
}
