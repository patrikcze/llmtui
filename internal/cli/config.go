package cli

import (
	"encoding/json"
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
	cmd.AddCommand(newConfigInitCmd(r), newConfigShowCmd(r), newConfigPathCmd(r), newConfigSchemaCmd(), newConfigValidateCmd(r))
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

func newConfigSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print every config key with its type, default and accepted values (JSON)",
		Long: "Print every key config.yaml can set as JSON: its type, llmtui's default,\n" +
			"the accepted values of fixed-choice keys, and whether it holds a secret.\n" +
			"Editors such as the macOS setup app use it to offer every setting.",
		// The schema describes llmtui itself, not a config file, so a broken
		// config must not stop it from printing.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				Fields []config.SchemaField `json:"fields"`
			}{config.Schema()})
		},
	}
}

func newConfigValidateCmd(r *Root) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the config file for problems llmtui would hit later",
		Long: "Load the config (as every command does) and report what loading accepts\n" +
			"but llmtui would fail on or ignore later: a default_provider with no\n" +
			"provider block, an unknown provider type, values outside a key's accepted\n" +
			"set, malformed durations and unknown keys. Exits non-zero on errors;\n" +
			"warnings alone do not fail.",
		RunE: func(cmd *cobra.Command, args []string) error {
			problems := config.Check(r.cfg, r.viper.AllKeys())
			errors := 0
			for _, p := range problems {
				if p.Error {
					errors++
				}
			}
			if asJSON {
				if problems == nil {
					problems = []config.Problem{}
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(struct {
					Problems []config.Problem `json:"problems"`
				}{problems}); err != nil {
					return fmt.Errorf("encode problems: %w", err)
				}
			} else {
				for _, p := range problems {
					level := "warning"
					if p.Error {
						level = "error"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s: %s\n", level, p.Key, p.Message)
				}
				if len(problems) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "configuration is valid")
				}
			}
			if errors > 0 {
				cmd.SilenceUsage = true
				return fmt.Errorf("configuration has %d error(s)", errors)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the problems as JSON")
	return cmd
}
