package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/tui"
)

func newChatCmd(r *Root) *cobra.Command {
	var resumeName string
	var cont bool

	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive chat session",
		RunE: func(cmd *cobra.Command, args []string) error {
			prov, err := app.BuildActiveProvider(r.cfg)
			if err != nil {
				return fmt.Errorf("start chat: %w", err)
			}
			cfgPath, _ := r.configPath()
			opts := tui.Options{
				Config:     r.cfg,
				Provider:   prov,
				Model:      r.cfg.ActiveModel(),
				ConfigPath: cfgPath,
			}

			if resumeName != "" || cont {
				dir, err := r.historyDir()
				if err != nil {
					return fmt.Errorf("resume: %w", err)
				}
				var name string
				var sess history.Session
				if cont {
					name, sess, err = history.Latest(dir)
				} else {
					name = resumeName
					sess, err = history.Load(dir, name)
				}
				if err != nil {
					return fmt.Errorf("resume: %w", err)
				}
				opts.ResumeSession = &sess
				opts.ResumeSessionName = name
			}

			return tui.Run(opts)
		},
	}

	cmd.Flags().StringVar(&resumeName, "resume", "", "resume a saved session by name (see `llmtui history`)")
	cmd.Flags().BoolVarP(&cont, "continue", "c", false, "resume the most recently saved session")
	cmd.MarkFlagsMutuallyExclusive("resume", "continue")
	return cmd
}
