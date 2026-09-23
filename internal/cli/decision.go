package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/patrikcze/llmtui/internal/decision"
	"github.com/spf13/cobra"
)

func newDecisionCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decision",
		Short: "Manage local structured decision models",
		Long:  "Manage optional Laya decision checkpoints. SafeTensors source downloads are kept separate from executable runtime artifacts.",
	}
	cmd.AddCommand(
		newDecisionModelsCmd(r),
		newDecisionPullCmd(r),
		newDecisionInspectCmd(r),
		newDecisionVerifyCmd(r),
		newDecisionRemoveCmd(r),
		newDecisionRuntimeCmd(r),
		newDecisionPredictCmd(r),
	)
	return cmd
}

func newDecisionManager(r *Root) (*decision.ModelManager, error) {
	return decision.NewModelManager(decision.ModelManagerOptions{RootDir: r.cfg.DecisionEngine.Laya.ModelDir})
}

func newDecisionModelsCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List available and installed Laya checkpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			installed, err := manager.ListInstalled()
			if err != nil {
				return fmt.Errorf("list decision models: %w", err)
			}
			byAlias := make(map[string]decision.Installation, len(installed))
			for _, item := range installed {
				if item.Valid {
					byAlias[item.Manifest.Model] = item
				}
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "MODEL\tSTATUS\tREVISION\tRUNTIME")
			for _, descriptor := range manager.Catalog() {
				item, ok := byAlias[descriptor.Alias]
				status, revision, runtimeStatus := "not installed", "", "unavailable"
				if ok {
					status = "installed"
					revision = item.Manifest.Source.Revision
					if item.Manifest.Runtime.Ready {
						runtimeStatus = item.Manifest.Runtime.Format
					} else {
						runtimeStatus = item.Manifest.Runtime.Format + " (external loader required)"
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", "laya:"+descriptor.Alias, status, revision, runtimeStatus)
			}
			return w.Flush()
		},
	}
}

func newDecisionPullCmd(r *Root) *cobra.Command {
	var revision string
	var repair bool
	cmd := &cobra.Command{
		Use:   "pull laya:<model>",
		Short: "Download and verify one Laya source checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			result, err := manager.Pull(ctx, args[0], decision.PullOptions{
				Revision: revision,
				Repair:   repair,
				Progress: func(p decision.Progress) {
					if p.Done {
						fmt.Fprintf(cmd.ErrOrStderr(), "installed %s\n", p.Model)
						return
					}
					if p.Total > 0 {
						fmt.Fprintf(cmd.ErrOrStderr(), "downloading %s: %d/%d bytes\n", p.Artifact, p.Downloaded, p.Total)
					}
				},
			})
			if err != nil {
				return fmt.Errorf("pull decision model: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Model: %s\nRevision: %s\nPath: %s\nRuntime: %s (ready=%t)\n", result.ID, result.Manifest.Source.Revision, result.Path, result.Manifest.Runtime.Format, result.Manifest.Runtime.Ready)
			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "pin a Hugging Face revision or commit SHA")
	cmd.Flags().BoolVar(&repair, "repair", false, "remove and reinstall an invalid existing installation")
	return cmd
}

func newDecisionInspectCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect laya:<model>",
		Short: "Inspect installed Laya model metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			items, err := manager.Inspect(args[0])
			if err != nil {
				return fmt.Errorf("inspect decision model: %w", err)
			}
			if len(items) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is not installed\n", args[0])
				return nil
			}
			for _, item := range items {
				fmt.Fprintf(cmd.OutOrStdout(), "Model: %s\nRepository: %s\nRevision: %s\nPath: %s\nValid: %t\nRuntime: %s (ready=%t)\n", item.ID, item.Manifest.Source.Repository, item.Manifest.Source.Revision, item.Path, item.Valid, item.Manifest.Runtime.Format, item.Manifest.Runtime.Ready)
			}
			return nil
		},
	}
}

func newDecisionVerifyCmd(r *Root) *cobra.Command {
	return &cobra.Command{
		Use:   "verify laya:<model>",
		Short: "Verify installed Laya artifact checksums",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			items, err := manager.Verify(args[0])
			if err != nil {
				return fmt.Errorf("verify decision model: %w", err)
			}
			if len(items) == 0 {
				return fmt.Errorf("decision model %s is not installed", args[0])
			}
			for _, item := range items {
				status := "FAILED"
				if item.Valid {
					status = "OK"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", status, item.ID, item.Manifest.Source.Revision)
				if !item.Valid {
					return fmt.Errorf("decision model %s failed integrity verification", item.ID)
				}
			}
			return nil
		},
	}
}

func newDecisionRemoveCmd(r *Root) *cobra.Command {
	var revision string
	cmd := &cobra.Command{
		Use:   "remove laya:<model>",
		Short: "Remove one installed Laya revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if revision == "" {
				return fmt.Errorf("--revision is required; removal never deletes all revisions implicitly")
			}
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			if err := manager.Remove(args[0], revision); err != nil {
				return fmt.Errorf("remove decision model: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s revision %s\n", args[0], revision)
			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "installed commit SHA to remove")
	return cmd
}
