package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/selfupdate"
)

// newSelfCmd builds the `llmtui self` command tree. Like `version`, every
// command here bypasses configuration loading: a broken config.yaml must not
// stop a user from checking for, installing, or updating llmtui — updating
// may be what fixes the config.
func newSelfCmd(version, commit, date string) *cobra.Command {
	build := selfupdate.CurrentBuild(version, commit, date)

	cmd := &cobra.Command{
		Use:   "self",
		Short: "Install, check, and update the llmtui binary",
		Long: "Manage the llmtui installation itself.\n\n" +
			"Updates come only from the official GitHub Releases of\n" +
			"github.com/" + selfupdate.RepoOwner + "/" + selfupdate.RepoName + ", are verified against the\n" +
			"release's SHA-256 checksums, and are installed with a staged,\n" +
			"transactional replacement that never destroys the working binary\n" +
			"until the replacement is downloaded, verified and validated.",
		// No configuration required for any `self` subcommand.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	cmd.AddCommand(
		newSelfCheckCmd(build),
		newSelfUpdateCmd(build),
		newSelfInstallCmd(build),
		newSelfPathCmd(build),
	)
	return cmd
}

func newSelfCheckCmd(build selfupdate.BuildInfo) *cobra.Command {
	var includePre bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check GitHub Releases for a newer llmtui (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()

			res, err := selfupdate.Check(ctx, selfupdate.NewClient(), build, includePre)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			cur := res.CurrentRaw
			if res.IsDevBuild {
				cur += " (development/source build)"
			}
			fmt.Fprintf(out, "Current version: %s\n", cur)
			fmt.Fprintf(out, "Latest version:  %s\n", res.LatestVersion)
			fmt.Fprintf(out, "Platform:        %s/%s\n", res.OS, res.Arch)
			if res.ExpectedAsset != "" {
				fmt.Fprintf(out, "Release asset:   %s\n", res.ExpectedAsset)
			}
			switch {
			case !res.PlatformOK:
				fmt.Fprintf(out, "Status:          %s/%s has no release archive; build from source\n", res.OS, res.Arch)
			case res.IsDevBuild:
				fmt.Fprintf(out, "Status:          cannot compare — this is a development build\n\n")
				fmt.Fprintf(out, "Install a release with 'llmtui self install', or download from\n")
				fmt.Fprintf(out, "https://github.com/%s/%s/releases\n", selfupdate.RepoOwner, selfupdate.RepoName)
			case res.UpdateAvailable:
				fmt.Fprintf(out, "Status:          update available\n\n")
				fmt.Fprintf(out, "Run 'llmtui self update' to update.\n")
			default:
				fmt.Fprintf(out, "Status:          up to date\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&includePre, "pre", false, "consider prereleases too")
	return cmd
}

func newSelfPathCmd(build selfupdate.BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show which llmtui executable is running (offline)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := selfupdate.InspectPath(build)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Executable: %s\n", info.Executable)
			fmt.Fprintf(out, "Resolved:   %s\n", info.Resolved)
			fmt.Fprintf(out, "Version:    %s\n", info.Version)
			fmt.Fprintf(out, "Scope:      %s\n", info.Scope)
			if m := info.Manifest; m != nil {
				fmt.Fprintf(out, "Installed:  %s (%s) from %s\n", m.Version, m.InstalledBy, orNone(m.Asset))
				fmt.Fprintf(out, "Prefix:     %s\n", m.Prefix)
			}
			return nil
		},
	}
}

func newSelfUpdateCmd(build selfupdate.BuildInfo) *cobra.Command {
	var (
		assumeYes  bool
		dryRun     bool
		force      bool
		dest       string
		includePre bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download, verify and install the latest llmtui release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			resolvedExe, err := resolvedExecutable()
			if err != nil {
				return err
			}
			target, err := selfupdate.TargetForRunningExe(resolvedExe, build.Version, dest, force)
			if err != nil {
				return err
			}
			selfupdate.CleanStaleBackups(target)

			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Minute)
			defer cancel()

			client := selfupdate.NewClient()
			plan, err := selfupdate.PlanUpdate(ctx, client, build, target, includePre)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "Current: %s\n", currentLabel(build, plan.IsDevBuild))
			fmt.Fprintf(out, "Latest:  %s\n", plan.Latest)
			fmt.Fprintf(out, "Asset:   %s\n", plan.Archive.Name)
			fmt.Fprintf(out, "Target:  %s\n", plan.Target.BinPath)
			fmt.Fprintln(out)

			if plan.AlreadyCurrent() && !force {
				fmt.Fprintf(out, "Already up to date (%s). Use --force to reinstall.\n", plan.Latest)
				return nil
			}
			if dryRun {
				fmt.Fprintf(out, "Would download, verify and install %s.\nNo files changed.\n", plan.Latest)
				return nil
			}
			if !assumeYes {
				ok, err := confirm(cmd, fmt.Sprintf("Install %s into %s?", plan.Latest, plan.Target.Prefix))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "Aborted.")
					return nil
				}
			}

			if err := selfupdate.ExecuteUpdate(ctx, client, plan, out); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nUpdated to %s. The working binary was replaced only after verification.\n", plan.Latest)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&assumeYes, "yes", false, "do not prompt for confirmation")
	f.BoolVar(&dryRun, "dry-run", false, "show what would happen without changing any files")
	f.BoolVar(&force, "force", false, "reinstall even if already current or running unmanaged")
	f.StringVar(&dest, "dest", "", "install into this root instead of the detected location")
	f.BoolVar(&includePre, "pre", false, "allow updating to a prerelease")
	return cmd
}

func newSelfInstallCmd(build selfupdate.BuildInfo) *cobra.Command {
	var (
		system    bool
		user      bool
		dest      string
		assumeYes bool
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the currently running llmtui into a managed location",
		Long: "Copies the running llmtui binary (and its bundled llama.cpp runtime,\n" +
			"if present) into a managed location.\n\n" +
			"  llmtui self install            user install, no admin rights needed\n" +
			"  llmtui self install --system   all users (needs an elevated shell)\n\n" +
			"llmtui never invokes sudo/UAC and never edits shell profiles.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if system && user {
				return fmt.Errorf("--system and --user are mutually exclusive")
			}
			out := cmd.OutOrStdout()

			scope := selfupdate.ScopeUser
			if system {
				scope = selfupdate.ScopeSystem
			}
			target, err := selfupdate.TargetForScope(scope, dest)
			if err != nil {
				return err
			}
			selfupdate.CleanStaleBackups(target)

			resolvedExe, err := resolvedExecutable()
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "Source: %s\n", resolvedExe)
			fmt.Fprintf(out, "Scope:  %s\n", target.Scope)
			fmt.Fprintf(out, "Target: %s\n", target.BinPath)
			fmt.Fprintln(out)

			if dryRun {
				fmt.Fprintf(out, "Would install %s to %s.\nNo files changed.\n", build.Version, target.BinPath)
				return nil
			}
			if !assumeYes {
				ok, err := confirm(cmd, fmt.Sprintf("Install into %s?", target.Prefix))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "Aborted.")
					return nil
				}
			}

			res, err := selfupdate.InstallCurrent(resolvedExe, target, build.Version, out)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "\nInstalled %s to %s\n", build.Version, res.Target.BinPath)
			if res.RuntimeNote != "" {
				fmt.Fprintf(out, "Note: %s\n", res.RuntimeNote)
			}
			binDir := filepath.Dir(res.Target.BinPath)
			changed, note, err := selfupdate.UpdatePath(res.Target)
			switch {
			case err != nil:
				fmt.Fprintf(out, "PATH not updated automatically: %v\n", err)
				warnIfNotOnPath(out, binDir)
			case changed:
				fmt.Fprintf(out, "%s\n", note)
			default:
				warnIfNotOnPath(out, binDir)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&system, "system", false, "install for all users (needs elevation)")
	f.BoolVar(&user, "user", false, "install for the current user (default)")
	f.StringVar(&dest, "dest", "", "install into this root instead of the standard location")
	f.BoolVar(&assumeYes, "yes", false, "do not prompt for confirmation")
	f.BoolVar(&dryRun, "dry-run", false, "show what would happen without changing any files")
	return cmd
}

// --- shared helpers ---

func resolvedExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

func currentLabel(build selfupdate.BuildInfo, isDev bool) string {
	if isDev {
		return build.Version + " (development build)"
	}
	return build.Version
}

func orNone(s string) string {
	if s == "" {
		return "(no asset)"
	}
	return s
}

// confirm prompts on stdin. When stdin is not interactive it refuses rather
// than blocking a CI job forever.
func confirm(cmd *cobra.Command, prompt string) (bool, error) {
	if !stdinIsInteractive() {
		return false, fmt.Errorf("%s\nstdin is not interactive; re-run with --yes to proceed non-interactively", prompt)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", prompt)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

func stdinIsInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func warnIfNotOnPath(w interface{ Write([]byte) (int, error) }, dir string) {
	if selfupdate.DirOnPath(dir) {
		return
	}
	fmt.Fprintf(w, "\nWarning: %s is not on your PATH.\n", dir)
	fmt.Fprintf(w, "Add it to your shell profile to run 'llmtui' directly.\n")
}
