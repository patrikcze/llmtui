package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/patrikcze/llmtui/internal/runtime"
	"github.com/spf13/cobra"
)

// newRuntimeCmd creates the runtime management command tree.
func newRuntimeCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Manage llama.cpp runtime installations",
		Long:  "Install, list, verify, or uninstall llama.cpp runtime libraries.",
	}

	cmd.AddCommand(
		newRuntimeInstallCmd(r),
		newRuntimeListCmd(r),
		newRuntimeVerifyCmd(r),
		newRuntimeUninstallCmd(r),
	)

	return cmd
}

// newRuntimeInstallCmd creates the runtime install command.
func newRuntimeInstallCmd(r *Root) *cobra.Command {
	var dest string
	var backend string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install llama.cpp runtime for the current platform",
		Long:  "Downloads, verifies, and installs the llama.cpp runtime libraries.\n\nThe runtime is installed to the user data directory by default.\nUse --dest to override the installation directory.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			opts := runtime.InstallOptions{
				Dest:    dest,
				Backend: backend,
			}

			fmt.Fprintf(os.Stderr, "Installing llama.cpp runtime...\n")
			fmt.Fprintf(os.Stderr, "Platform: %s\n", runtime.CurrentPlatform())
			if dest != "" {
				fmt.Fprintf(os.Stderr, "Destination: %s\n", dest)
			}

			result, err := runtime.Install(ctx, opts)
			if err != nil {
				return fmt.Errorf("install runtime: %w", err)
			}

			fmt.Fprintf(os.Stderr, "\n")
			if result.Installed {
				fmt.Fprintf(os.Stdout, "✓ Runtime %s installed successfully\n", result.Tag)
			} else {
				fmt.Fprintf(os.Stdout, "✓ Runtime %s already installed and valid\n", result.Tag)
			}
			fmt.Fprintf(os.Stdout, "  Platform: %s\n", result.Platform)
			fmt.Fprintf(os.Stdout, "  Backend: %s\n", backend)
			fmt.Fprintf(os.Stdout, "  Directory: %s\n", result.Dir)

			return nil
		},
	}

	cmd.Flags().StringVar(&dest, "dest", "", "override installation directory (for testing or advanced use)")
	cmd.Flags().StringVar(&backend, "backend", "cpu", "runtime backend (cpu, vulkan, cuda)")

	return cmd
}

// newRuntimeListCmd creates the runtime list command.
func newRuntimeListCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed runtime",
		Long:  "Shows the installation status of the llama.cpp runtime for the current platform.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runtime.List()
			if err != nil {
				return fmt.Errorf("list runtime: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Runtime: %s\n", result.Tag)
			fmt.Fprintf(os.Stdout, "Platform: %s\n", result.Platform)
			if result.Backend != "" {
				fmt.Fprintf(os.Stdout, "Backend: %s\n", result.Backend)
			}
			fmt.Fprintf(os.Stdout, "Directory: %s\n", result.Dir)

			if !result.Installed {
				fmt.Fprintf(os.Stdout, "Status: not installed\n")
				fmt.Fprintf(os.Stdout, "\nRun 'llmtui runtime install' to install the runtime.\n")
				return nil
			}

			if result.Valid {
				fmt.Fprintf(os.Stdout, "Status: installed and verified\n")
			} else {
				fmt.Fprintf(os.Stdout, "Status: installed but invalid\n")
				if len(result.Errors) > 0 {
					fmt.Fprintf(os.Stdout, "\nErrors:\n")
					for _, err := range result.Errors {
						fmt.Fprintf(os.Stdout, "  - %s\n", err)
					}
				}
				fmt.Fprintf(os.Stdout, "\nRun 'llmtui runtime install' to reinstall or 'llmtui runtime verify' for details.\n")
			}

			return nil
		},
	}

	return cmd
}

// newRuntimeVerifyCmd creates the runtime verify command.
func newRuntimeVerifyCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify runtime installation integrity",
		Long:  "Performs full SHA256 verification of all runtime files.\n\nThis is more thorough than the quick check used at startup.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stderr, "Verifying runtime installation...\n")

			result, err := runtime.Verify()
			if err != nil {
				return fmt.Errorf("verify runtime: %w", err)
			}

			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stdout, "Runtime: %s\n", result.Tag)
			fmt.Fprintf(os.Stdout, "Platform: %s\n", result.Platform)
			fmt.Fprintf(os.Stdout, "Backend: %s\n", result.Backend)
			fmt.Fprintf(os.Stdout, "Directory: %s\n", result.Dir)
			fmt.Fprintf(os.Stdout, "\n")

			if result.Valid {
				fmt.Fprintf(os.Stdout, "✓ All files verified successfully\n")
				return nil
			}

			fmt.Fprintf(os.Stdout, "✗ Verification failed\n")
			if len(result.Result.MissingFiles) > 0 {
				fmt.Fprintf(os.Stdout, "\nMissing files:\n")
				for _, f := range result.Result.MissingFiles {
					fmt.Fprintf(os.Stdout, "  - %s\n", f)
				}
			}
			if len(result.Result.BadHashes) > 0 {
				fmt.Fprintf(os.Stdout, "\nFiles with incorrect hashes:\n")
				for _, f := range result.Result.BadHashes {
					fmt.Fprintf(os.Stdout, "  - %s\n", f)
				}
			}
			if len(result.Result.Warnings) > 0 {
				fmt.Fprintf(os.Stdout, "\nWarnings:\n")
				for _, w := range result.Result.Warnings {
					fmt.Fprintf(os.Stdout, "  - %s\n", w)
				}
			}

			fmt.Fprintf(os.Stdout, "\nRun 'llmtui runtime install' to reinstall.\n")

			return fmt.Errorf("verification failed")
		},
	}

	return cmd
}

// newRuntimeUninstallCmd creates the runtime uninstall command.
func newRuntimeUninstallCmd(r *Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall llama.cpp runtime",
		Long:  "Removes the installed llama.cpp runtime for the current platform.\n\nOnly removes files in the embedded trusted manifest and refuses directories containing unmanaged files.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get runtime info first
			listResult, err := runtime.List()
			if err != nil {
				return fmt.Errorf("check runtime status: %w", err)
			}

			if !listResult.Installed {
				fmt.Fprintf(os.Stdout, "Runtime %s is not installed\n", listResult.Tag)
				return nil
			}

			fmt.Fprintf(os.Stderr, "Uninstalling runtime %s from %s...\n", listResult.Tag, listResult.Dir)

			if err := runtime.Uninstall(runtime.UninstallOptions{}); err != nil {
				return fmt.Errorf("uninstall runtime: %w", err)
			}

			fmt.Fprintf(os.Stdout, "✓ Runtime %s uninstalled successfully\n", listResult.Tag)

			return nil
		},
	}

	return cmd
}
