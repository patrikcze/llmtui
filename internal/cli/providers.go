package cli

import (
	"context"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
)

func newProvidersCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List configured providers and their status",
		RunE: func(cmd *cobra.Command, args []string) error {
			names := make([]string, 0, len(r.cfg.Providers))
			for name := range r.cfg.Providers {
				names = append(names, name)
			}
			sort.Strings(names)

			active := r.cfg.ActiveProviderName()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tBASE URL\tSTATUS")
			for _, name := range names {
				pc := r.cfg.Providers[name]
				status := "unreachable"
				prov, err := app.BuildProvider(name, pc, r.cfg.Network)
				if err == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
					if prov.HealthCheck(ctx) == nil {
						status = "online"
					}
					cancel()
				}
				marker := ""
				if name == active {
					marker = " *"
				}
				fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n", name, marker, pc.Type, pc.BaseURL, status)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "\n* active provider")
			return nil
		},
	}
}
