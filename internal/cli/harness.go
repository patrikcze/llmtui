package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/harness"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

func (r *Root) harnessStore() (harness.Store, error) {
	dir := r.harnessDir
	if dir == "" {
		base, err := config.Dir()
		if err != nil {
			return harness.Store{}, err
		}
		if r.cfgFile != "" {
			base = filepath.Dir(r.cfgFile)
		}
		dir = filepath.Join(base, "harness")
	}
	return harness.Store{Dir: dir}, nil
}

func harnessJSON(cmd *cobra.Command, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), terminaltext.Sanitize(string(data)))
	return err
}

func newHarnessCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{Use: "harness", Short: "Run restricted harness simulations and review opt-in guidance", Long: "Harness experiments are fixture simulations, not live agent benchmarks. No model tools, shell commands, MCP or external effects are executed. Recommendations require explicit adoption and chat --experimental-harness."}
	cmd.AddCommand(newHarnessRunCmd(r))
	cmd.AddCommand(&cobra.Command{Use: "inspect [experiment-id]", Short: "Inspect active selection, evidence and component changes as JSON", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := r.harnessStore()
		if err != nil {
			return err
		}
		state, err := store.Load()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return harnessJSON(cmd, state)
		}
		report, err := state.Report(args[0])
		if err != nil {
			return err
		}
		diffs := map[string][]harness.Edit{}
		for _, s := range report.Snapshots {
			diffs[s.ID] = s.Diff(report.Snapshots[0])
		}
		return harnessJSON(cmd, struct {
			Report harness.Report            `json:"report"`
			Diffs  map[string][]harness.Edit `json:"component_edits"`
		}{report, diffs})
	}})
	for _, action := range []string{"adopt", "revert", "disable", "reflect", "delete-experience", "reject"} {
		sub := &cobra.Command{Use: action, Short: "Explicitly " + action + " harness state", Args: cobra.NoArgs}
		if slices.Contains([]string{"adopt", "reflect", "delete-experience", "reject"}, action) {
			sub.Use += " id"
			sub.Args = cobra.ExactArgs(1)
		}
		sub.RunE = func(cmd *cobra.Command, args []string) error {
			store, err := r.harnessStore()
			if err != nil {
				return err
			}
			err = store.Update(cmd.Context(), func(s *harness.State) error {
				switch action {
				case "adopt":
					return s.Adopt(args[0])
				case "revert":
					return s.Revert()
				case "disable":
					s.Disable()
				case "reflect":
					return s.Reflect(args[0], time.Now())
				case "delete-experience":
					before := len(s.Experiences)
					s.Experiences = slices.DeleteFunc(s.Experiences, func(e harness.Experience) bool { return e.ID == args[0] })
					if before == len(s.Experiences) {
						return errors.New("experience not found")
					}
				case "reject":
					if s.Active != nil && s.Active.Report == args[0] {
						return errors.New("disable or revert the active recommendation before rejecting its report")
					}
					for _, p := range s.Previous {
						if p != nil && p.Report == args[0] {
							return errors.New("report is retained for rollback; disable first")
						}
					}
					before := len(s.Reports)
					s.Reports = slices.DeleteFunc(s.Reports, func(report harness.Report) bool { return report.ID == args[0] })
					if before == len(s.Reports) {
						return errors.New("experiment not found")
					}
					s.Experiences = slices.DeleteFunc(s.Experiences, func(e harness.Experience) bool { return e.Report == args[0] })
				}
				return nil
			})
			if err != nil {
				return err
			}
			return harnessJSON(cmd, map[string]string{"status": action, "scope": "next explicitly opted-in chat; running sessions are unchanged"})
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

func newHarnessRunCmd(r *Root) *cobra.Command {
	var protocol string
	var modelProposals bool
	cmd := &cobra.Command{Use: "run scenario", Short: "Compare baseline, invariant and domain candidates in fresh fixtures", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !slices.Contains(harness.Scenarios(), args[0]) {
			return fmt.Errorf("unsupported scenario; choose %v (live workspace probes are unsupported)", harness.Scenarios())
		}
		if protocol != "native" && protocol != "fenced" {
			return errors.New("protocol must be native or fenced")
		}
		store, err := r.harnessStore()
		if err != nil {
			return err
		}
		state, err := store.Load()
		if err != nil {
			return err
		}
		opts := harness.ExperimentOptions{Scenario: args[0], Protocol: protocol, Compatibility: harness.Compatibility{Provider: r.cfg.ActiveProviderName(), Model: r.cfg.ActiveModel()}, Experiences: state.Experiences}
		if modelProposals {
			p, err := app.BuildActiveProvider(r.cfg)
			if err != nil {
				return err
			}
			defer func() { _ = provider.CloseProvider(p) }()
			opts.Compatibility.Runtime = provider.RuntimeFingerprintOf(p)
			opts.Proposer = &harness.Proposer{Provider: p, Model: r.cfg.ActiveModel(), Temperature: r.cfg.Chat.Temperature, TopP: r.cfg.Chat.TopP, MaxTokens: 1024}
		}
		report, err := harness.Run(cmd.Context(), opts)
		if err != nil {
			return err
		}
		if err := store.Update(cmd.Context(), func(s *harness.State) error { return s.AddReport(report) }); err != nil {
			return err
		}
		return harnessJSON(cmd, report)
	}}
	cmd.Flags().StringVar(&protocol, "protocol", "native", "fixture tool normalization: native or fenced")
	cmd.Flags().BoolVar(&modelProposals, "model-proposals", false, "explicitly use the configured provider for two tool-free proposal requests; probes remain simulations")
	return cmd
}

// Resolve the selection once per session. Ordinary startup does no state I/O.
func (r *Root) activeHarness(ctx context.Context, p provider.Provider) (*harness.Snapshot, error) {
	if !r.useHarness {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := r.harnessStore()
	if err != nil {
		return nil, err
	}
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	snapshot, err := state.ActiveSnapshot(harness.Compatibility{Provider: r.cfg.ActiveProviderName(), Model: r.cfg.ActiveModel(), Runtime: provider.RuntimeFingerprintOf(p)})
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}
