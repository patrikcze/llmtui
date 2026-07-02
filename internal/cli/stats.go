package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

func newStatsCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show all-time token usage from the usage log",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := r.historyDir()
			if err != nil {
				return err
			}
			records, err := history.ReadUsage(dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(records) == 0 {
				fmt.Fprintln(out, "no usage recorded yet — chat a bit first")
				return nil
			}

			days := history.AggregateByDay(records)
			w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "DAY\tREQUESTS\tPROMPT\tREPLY\tTOTAL")
			prompt, reply := 0, 0
			for _, d := range days {
				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n",
					d.Day, d.Requests, d.PromptTokens, d.CompletionTokens, d.TotalTokens())
				prompt += d.PromptTokens
				reply += d.CompletionTokens
			}
			if err := w.Flush(); err != nil {
				return err
			}

			totals := make([]int, len(days))
			for i, d := range days {
				totals[i] = d.TotalTokens()
			}
			fmt.Fprintf(out, "\n%s  tokens/day\n", components.Sparkline(totals, 40, false))
			fmt.Fprintf(out, "total: %d requests · prompt %d · reply %d · %d tokens\n",
				len(records), prompt, reply, prompt+reply)
			return nil
		},
	}
}
