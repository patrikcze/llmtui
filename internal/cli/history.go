package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/history"
)

func (r *Root) historyDir() (string, error) {
	if r.cfg.Chat.HistoryDir == "" {
		return "", fmt.Errorf("chat.history_dir is not configured")
	}
	return history.ExpandHome(r.cfg.Chat.HistoryDir)
}

func newHistoryCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "List saved chat sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := r.historyDir()
			if err != nil {
				return err
			}
			metas, err := history.List(dir)
			if err != nil {
				return err
			}
			if len(metas) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no saved sessions in %s\n", dir)
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSAVED\tPROVIDER\tMODEL\tMSGS\tTOKENS")
			for _, m := range metas {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
					m.Name, m.SavedAt.Format("2006-01-02 15:04"),
					m.Provider, m.Model, m.Messages, m.Tokens)
			}
			return w.Flush()
		},
	}
}
