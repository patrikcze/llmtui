package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
)

func newModelsCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List models available on the active provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			prov, err := app.BuildActiveProvider(r.cfg)
			if err != nil {
				return err
			}
			models, err := prov.ListModels(cmd.Context())
			if err != nil {
				return fmt.Errorf("list models on %s: %w", prov.Name(), err)
			}
			if len(models) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no models found on %s\n", prov.Name())
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tDESCRIPTION")
			for _, m := range models {
				fmt.Fprintf(w, "%s\t%s\n", m.ID, m.Description)
			}
			return w.Flush()
		},
	}
}
