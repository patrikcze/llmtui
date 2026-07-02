package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd(version, commit, date string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		// Version does not need config loading.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "llmtui %s (commit %s, built %s, %s/%s)\n",
				version, commit, date, runtime.GOOS, runtime.GOARCH)
		},
	}
}
