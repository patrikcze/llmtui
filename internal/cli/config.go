package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/patrikcze/llmtui/internal/config"
)

func newConfigCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage llmtui configuration",
	}
	cmd.AddCommand(newConfigInitCmd(r), newConfigShowCmd(r), newConfigPathCmd(r))
	return cmd
}

func (r *Root) configPath() (string, error) {
	if r.cfgFile != "" {
		return r.cfgFile, nil
	}
	return config.DefaultPath()
}

func newConfigInitCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write a starter config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := r.configPath()
			if err != nil {
				return err
			}
			if err := config.WriteDefault(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote config to %s\n", path)
			return nil
		},
	}
}

func newConfigShowCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective merged configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Redact secrets before printing.
			shown := config.RedactedCopy(r.cfg)
			out, err := yaml.Marshal(shown)
			if err != nil {
				return fmt.Errorf("encode config: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
}

func newConfigPathCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := r.configPath()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}
