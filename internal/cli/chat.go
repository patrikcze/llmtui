package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/tui"
)

func newChatCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive chat session",
		RunE: func(cmd *cobra.Command, args []string) error {
			prov, err := app.BuildActiveProvider(r.cfg)
			if err != nil {
				return fmt.Errorf("start chat: %w", err)
			}
			cfgPath, _ := r.configPath()
			return tui.Run(tui.Options{
				Config:     r.cfg,
				Provider:   prov,
				Model:      r.cfg.ActiveModel(),
				ConfigPath: cfgPath,
			})
		},
	}
}
