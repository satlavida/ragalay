package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
)

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [folder]",
		Short: "Make a folder searchable (creates .ragalay/ inside it)",
		Long: `Creates the .ragalay/ folder that holds ragalay's settings and index.
Defaults to the current folder. Running it again is safe.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			root, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			created, err := initRoot(cmd.Context(), root)
			if err != nil {
				return err
			}
			if !created {
				fmt.Fprintf(a.stdout, "%s is already set up for ragalay.\n", root)
				return nil
			}
			fmt.Fprintf(a.stdout, "Set up ragalay in %s\n\n", root)
			fmt.Fprintln(a.stdout, "Next:")
			fmt.Fprintln(a.stdout, "  ragalay folders add <folder>   choose what to index (default: everything here)")
			fmt.Fprintln(a.stdout, "  ragalay status                 see what ragalay knows")
			return nil
		},
	}
}

// initRoot creates .ragalay/ with a default config and an empty index. It
// reports whether anything was created; an existing setup is only migrated.
func initRoot(ctx context.Context, root string) (bool, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s is not a folder", root)
	}
	dir := filepath.Join(root, config.DirName)
	created := false
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	if err := os.MkdirAll(filepath.Join(dir, config.LogsDir), 0o755); err != nil {
		return false, err
	}
	cfg := config.Default()
	if _, err := os.Stat(config.Path(root)); errors.Is(err, os.ErrNotExist) {
		if err := config.Save(root, cfg); err != nil {
			return false, err
		}
	} else if cfg, err = config.Load(root); err != nil {
		return false, err
	}
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Migrate(ctx, db, cfg.Embed.Dim)
	})
	return created, err
}

func dbPath(root string) string { return filepath.Join(root, config.DirName, config.DBFile) }
