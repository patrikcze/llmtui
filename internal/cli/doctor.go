package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
	runtimemgr "github.com/patrikcze/llmtui/internal/runtime"
	"github.com/patrikcze/llmtui/internal/skill"
)

func newDoctorCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration and provider connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ok := func(msg string) { fmt.Fprintf(out, "  ✓ %s\n", msg) }
			warn := func(msg string) { fmt.Fprintf(out, "  ✗ %s\n", msg) }

			fmt.Fprintln(out, "config")
			path, err := r.configPath()
			if err != nil {
				warn(fmt.Sprintf("cannot resolve config path: %v", err))
			} else if _, statErr := os.Stat(path); statErr != nil {
				warn(fmt.Sprintf("no config file at %s (run `llmtui config init`)", path))
			} else {
				ok(fmt.Sprintf("config file found at %s", path))
			}

			active := r.cfg.ActiveProviderName()
			if _, _, found := r.cfg.ActiveProvider(); found {
				ok(fmt.Sprintf("active provider %q is configured", active))
			} else {
				warn(fmt.Sprintf("active provider %q is not configured", active))
			}
			if model := r.cfg.ActiveModel(); model != "" {
				ok(fmt.Sprintf("active model resolves to %q", model))
			} else {
				warn("no model configured (set default_model or --model)")
			}
			if tz := strings.TrimSpace(r.cfg.Context.Timezone); tz != "" {
				if _, tzErr := time.LoadLocation(tz); tzErr != nil {
					warn(fmt.Sprintf("context.timezone %q is not a valid IANA name (local_context kind=time will error): %v", tz, tzErr))
				} else {
					ok(fmt.Sprintf("context.timezone %q is valid", tz))
				}
			}

			fmt.Fprintln(out, "\nproviders")
			names := make([]string, 0, len(r.cfg.Providers))
			for name := range r.cfg.Providers {
				names = append(names, name)
			}
			sort.Strings(names)
			anyOnline := false
			for _, name := range names {
				pc := r.cfg.Providers[name]
				if pc.Type == "embedded" {
					pin, pinErr := runtimemgr.LoadPin()
					resolution, resolveErr := runtimemgr.Resolve(runtimemgr.ResolveConfig{
						ExplicitPath: pc.LibraryPath,
						YzmaLib:      os.Getenv("YZMA_LIB"),
						Pin:          pin,
					})
					if pinErr != nil {
						warn(fmt.Sprintf("%s runtime pin: %v", name, pinErr))
					} else if resolveErr != nil {
						warn(fmt.Sprintf("%s runtime: %v", name, resolveErr))
					} else if resolution.Verified {
						// Only managed tiers (3/4) reach here with Verified set:
						// resolveManaged already confirmed every required file is
						// present and hashes match the embedded pin.
						ok(fmt.Sprintf("%s runtime %s: %s, verified (%s)", name, pin.LlamaTag, resolution.TierName, resolution.Dir))
					} else {
						// Explicit overrides (tiers 1-2) and the legacy directory
						// (tier 5) are accepted without file-presence verification,
						// so report them as a warning rather than a checkmark —
						// the HealthCheck line below is authoritative on whether the
						// library files actually load.
						warn(fmt.Sprintf("%s runtime %s: %s, unverified override (%s)", name, pin.LlamaTag, resolution.TierName, resolution.Dir))
					}
				}
				prov, err := app.BuildProvider(name, pc, r.cfg.Network)
				if err != nil {
					warn(fmt.Sprintf("%s: %v", name, err))
					continue
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
				err = prov.HealthCheck(ctx)
				cancel()
				if err != nil {
					warn(fmt.Sprintf("%s: %v", name, err))
				} else {
					ok(fmt.Sprintf("%s is reachable", name))
					if name != "mock" {
						anyOnline = true
					}
				}
			}

			fmt.Fprintln(out, "\nskills")
			mgr := skill.NewManager(app.SkillOptionsForCWD(r.cfg))
			if !mgr.Enabled() {
				warn("skills are disabled (skills.enabled)")
			} else {
				skills := mgr.Skills()
				ok(fmt.Sprintf("%d skill(s) discovered", len(skills)))
				plugins := mgr.Plugins()
				enabledPlugins := 0
				invalidPlugins := 0
				for _, p := range plugins {
					switch {
					case p.Err != nil:
						invalidPlugins++
					case p.Enabled:
						enabledPlugins++
					}
				}
				ok(fmt.Sprintf("%d plugin(s) discovered, %d enabled", len(plugins), enabledPlugins))
				if invalidPlugins > 0 {
					warn(fmt.Sprintf("%d plugin(s) have an invalid manifest (see `llmtui doctor` output above or /plugins list)", invalidPlugins))
				}
				if mgr.ExposeCatalog() && len(skills) > 0 {
					ok(fmt.Sprintf("catalog exposed to tool-capable models (up to %d bytes per request)", app.SkillCatalogMaxBytes))
				}
				for _, w := range mgr.Warnings() {
					warn(w.String())
				}
			}

			fmt.Fprintln(out)
			if anyOnline {
				fmt.Fprintln(out, "ready: at least one real backend is online")
			} else {
				fmt.Fprintln(out, "no real backend online — `llmtui chat` will run in offline demo mode")
			}
			return nil
		},
	}
}
