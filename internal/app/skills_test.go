package app

import (
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
)

func TestSkillOptionsTranslatesConfig(t *testing.T) {
	cfg := &config.Config{
		Skills: config.SkillsConfig{
			Enabled:              true,
			Paths:                []string{"/extra/skills"},
			ExposeCatalogToModel: true,
			MaxActive:            3,
			MaxSkillKB:           16,
			MaxTotalActiveKB:     64,
		},
		Plugins: config.PluginsConfig{
			Paths:   []string{"/extra/plugins"},
			Enabled: []string{"jira-tools"},
		},
	}

	opts := SkillOptions(cfg, "/workspace")

	if !opts.Enabled || !opts.ExposeCatalog {
		t.Errorf("opts = %+v, want Enabled and ExposeCatalog true", opts)
	}
	if opts.Limits.MaxActive != 3 || opts.Limits.MaxSkillBytes != 16*1024 || opts.Limits.MaxTotalActiveBytes != 64*1024 {
		t.Errorf("opts.Limits = %+v, want kB values scaled to bytes", opts.Limits)
	}
	if len(opts.EnabledPlugins) != 1 || opts.EnabledPlugins[0] != "jira-tools" {
		t.Errorf("opts.EnabledPlugins = %v", opts.EnabledPlugins)
	}
	if len(opts.Paths.Extra) != 1 || opts.Paths.Extra[0] != "/extra/skills" {
		t.Errorf("opts.Paths.Extra = %v", opts.Paths.Extra)
	}
	if len(opts.Paths.ExtraPluginDirs) != 1 || opts.Paths.ExtraPluginDirs[0] != "/extra/plugins" {
		t.Errorf("opts.Paths.ExtraPluginDirs = %v", opts.Paths.ExtraPluginDirs)
	}
	if opts.Paths.WorkspaceDir == "" || opts.Paths.WorkspacePluginDir == "" {
		t.Errorf("opts.Paths workspace dirs should derive from the given workspace root, got %+v", opts.Paths)
	}
}

func TestSkillOptionsForCWDUsesWorkingDirectory(t *testing.T) {
	cfg := &config.Config{Skills: config.SkillsConfig{Enabled: true}}
	opts := SkillOptionsForCWD(cfg)
	if opts.Paths.WorkspaceDir == "" {
		t.Error("SkillOptionsForCWD should resolve a workspace-scoped skills dir from the current directory")
	}
}
