package app

import (
	"os"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/skill"
)

// SkillCatalogMaxBytes bounds the compact skill catalog text added to
// prompts when a config's skills.expose_catalog_to_model is on.
const SkillCatalogMaxBytes = 4096

// SkillOptions translates the config's skills/plugins sections into
// skill.Options. The default discovery paths derive from the platform
// config dir and the given workspace root; pass "" (or call
// SkillOptionsForCWD) when there is no workspace-scoped caller. Shared by
// the TUI (interactive discovery/activation) and `llmtui doctor` (read-only
// reporting) so both see identical discovery paths and limits.
func SkillOptions(cfg *config.Config, workspaceRoot string) skill.Options {
	paths := skill.DefaultPaths(workspaceRoot)
	paths.Extra = cfg.Skills.Paths
	paths.ExtraPluginDirs = cfg.Plugins.Paths
	return skill.Options{
		Enabled: cfg.Skills.Enabled,
		Paths:   paths,
		Limits: skill.Limits{
			MaxSkillBytes:       cfg.Skills.MaxSkillKB * 1024,
			MaxActive:           cfg.Skills.MaxActive,
			MaxTotalActiveBytes: cfg.Skills.MaxTotalActiveKB * 1024,
		},
		EnabledPlugins: cfg.Plugins.Enabled,
		ExposeCatalog:  cfg.Skills.ExposeCatalogToModel,
	}
}

// SkillOptionsForCWD calls SkillOptions with the process's current working
// directory as the workspace root, matching how a launched llmtui resolves
// its own workspace-scoped skill/plugin directories.
func SkillOptionsForCWD(cfg *config.Config) skill.Options {
	wd, _ := os.Getwd()
	return SkillOptions(cfg, wd)
}
