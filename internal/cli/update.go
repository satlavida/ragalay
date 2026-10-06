package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/update"
)

func (a *app) updateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Install the latest ragalay release",
		Long: `Downloads the newest ragalay from GitHub releases, checks it against the
release's checksums, and replaces this program. Your index and models are kept.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			rel, err := update.Latest(ctx)
			if errors.Is(err, update.ErrNoRelease) {
				fmt.Fprintln(a.stdout, "No release has been published yet.")
				return nil
			}
			if err != nil {
				return fmt.Errorf("could not check for updates: %w", err)
			}
			if Version != "dev" && !update.Newer(rel.Tag, Version) {
				fmt.Fprintf(a.stdout, "ragalay %s is up to date.\n", Version)
				return nil
			}
			if check {
				fmt.Fprintf(a.stdout, "ragalay %s is available (you have %s). Run \"ragalay update\" to install it.\n", rel.Tag, Version)
				return nil
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
			cache, err := setup.CacheDir()
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Updating ragalay %s -> %s\n", Version, rel.Tag)
			rep := &termReporter{w: a.stdout}
			err = update.Apply(ctx, rel, exe, filepath.Join(cache, "updates"), rep.Progress)
			rep.endLine()
			if err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "Updated to %s. Start ragalay again to use it.\n", rel.Tag)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only say whether an update is available")
	return cmd
}
