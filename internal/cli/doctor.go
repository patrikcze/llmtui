package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
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

			fmt.Fprintln(out, "\nproviders")
			names := make([]string, 0, len(r.cfg.Providers))
			for name := range r.cfg.Providers {
				names = append(names, name)
			}
			sort.Strings(names)
			anyOnline := false
			for _, name := range names {
				pc := r.cfg.Providers[name]
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
