// Package cli defines the llmtui command tree.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/patrikcze/llmtui/internal/config"
)

// Root bundles state shared by all commands.
type Root struct {
	cfgFile string
	viper   *viper.Viper
	cfg     *config.Config
}

// NewRootCmd builds the llmtui command tree.
func NewRootCmd(version, commit, date string) *cobra.Command {
	r := &Root{}

	cmd := &cobra.Command{
		Use:           "llmtui",
		Short:         "A premium terminal UI for chatting with local LLMs",
		Long:          "llmtui is a fast, keyboard-first terminal UI for chatting with local\nand OpenAI-compatible LLM backends such as Ollama, LM Studio, vLLM and llama.cpp.",
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return r.load(cmd)
		},
	}

	pf := cmd.PersistentFlags()
	pf.StringVar(&r.cfgFile, "config", "", "path to config file")
	pf.String("provider", "", "provider to use (ollama, lmstudio, openai_compatible, embedded, mock)")
	pf.String("model", "", "model to use (a local .gguf file path for --provider embedded)")
	pf.String("base-url", "", "override the provider base URL")
	pf.String("api-key", "", "override the provider API key")
	pf.Float64("temperature", 0, "sampling temperature")
	pf.Float64("top-p", 0, "nucleus sampling top-p")
	pf.Int("max-tokens", 0, "maximum tokens to generate")
	pf.String("system", "", "system prompt")
	pf.String("theme", "", "UI theme")
	pf.Bool("no-stream", false, "disable streaming responses")
	pf.Bool("debug", false, "enable debug output")
	pf.Int("context-size", 0, "context window for the embedded provider (0 = bounded model default, max 8192)")
	pf.Int("gpu-layers", 0, "GPU layers to offload for the embedded provider (-1 = all, 0 = CPU only)")

	cmd.AddCommand(
		newChatCmd(r),
		newConfigCmd(r),
		newProvidersCmd(r),
		newModelsCmd(r),
		newDoctorCmd(r),
		newHistoryCmd(r),
		newStatsCmd(r),
		newVersionCmd(version, commit, date),
	)

	return cmd
}

// load merges flags, environment, config file and defaults (in that order).
func (r *Root) load(cmd *cobra.Command) error {
	v, err := config.NewViper(r.cfgFile)
	if err != nil {
		return err
	}

	bindings := map[string]string{
		"provider":           "provider",
		"model":              "model",
		"base_url":           "base-url",
		"api_key":            "api-key",
		"chat.temperature":   "temperature",
		"chat.top_p":         "top-p",
		"chat.max_tokens":    "max-tokens",
		"chat.system_prompt": "system",
		"ui.theme":           "theme",
		"no_stream":          "no-stream",
		"debug":              "debug",
		"context_size":       "context-size",
		"gpu_layers":         "gpu-layers",
	}
	flags := cmd.Flags()
	for key, flag := range bindings {
		f := flags.Lookup(flag)
		if f == nil {
			f = cmd.Root().PersistentFlags().Lookup(flag)
		}
		if f == nil {
			continue
		}
		// Only bind flags the user actually set, so unset flags do not
		// clobber env/file values with zero values.
		if f.Changed {
			if err := v.BindPFlag(key, f); err != nil {
				return fmt.Errorf("bind flag %s: %w", flag, err)
			}
		}
	}

	cfg, err := config.Load(v)
	if err != nil {
		return err
	}
	r.viper = v
	r.cfg = cfg
	return nil
}
