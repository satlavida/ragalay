package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
)

func (a *app) foldersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "folders",
		Short: "Choose which folders ragalay indexes",
		Long: `Folders must be inside the ragalay directory. With no folders listed,
ragalay indexes everything in the directory.`,
	}
	cmd.AddCommand(a.foldersListCmd(), a.foldersAddCmd(), a.foldersRemoveCmd())
	return cmd
}

// FolderInfo is one row of `folders list --json`.
type FolderInfo struct {
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	Documents int    `json:"documents"`
}

func (a *app) foldersListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the folders being indexed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			infos, err := folderInfos(cmd.Context(), root, cfg)
			if err != nil {
				return err
			}
			if asJSON {
				return a.printJSON(map[string]any{"whole_directory": len(cfg.Scan.Folders) == 0,
					"folders": infos, "kept": cfg.Scan.Keep})
			}
			if len(cfg.Scan.Folders) == 0 {
				fmt.Fprintln(a.stdout, "Indexing everything in", root)
			}
			for _, f := range infos {
				note := ""
				if !f.Exists {
					note = "  (missing!)"
				}
				fmt.Fprintf(a.stdout, "  %-30s %6d documents%s\n", f.Path, f.Documents, note)
			}
			for _, k := range cfg.Scan.Keep {
				fmt.Fprintf(a.stdout, "  %-30s (kept: searchable, not scanned for new files)\n", k)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func folderInfos(ctx context.Context, root string, cfg config.Config) ([]FolderInfo, error) {
	folders := config.EffectiveFolders(cfg.Scan.Folders)
	infos := make([]FolderInfo, len(folders))
	err := store.With(ctx, dbPath(root), func(db *sql.DB) error {
		for i, f := range folders {
			n, err := store.CountUnder(ctx, db, f)
			if err != nil {
				return err
			}
			fi, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(f)))
			infos[i] = FolderInfo{Path: f, Exists: statErr == nil && fi.IsDir(), Documents: n}
		}
		return nil
	})
	return infos, err
}

func (a *app) foldersAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <folder>...",
		Short: "Index a folder (and everything inside it)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			notes, err := addFolders(root, cwd, args)
			for _, n := range notes {
				fmt.Fprintln(a.stdout, n)
			}
			return err
		},
	}
}

// addFolders adds folders (paths relative to cwd) to the config.
func addFolders(root, cwd string, args []string) ([]string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	var notes []string
	folders := cfg.Scan.Folders
	for _, arg := range args {
		f, err := config.NormalizeFolder(root, cwd, arg)
		if err != nil {
			return notes, err
		}
		var note string
		folders, note = config.AddFolder(folders, f)
		notes = append(notes, note)
		// Scanning a folder again supersedes keeping it frozen.
		cfg.Scan.Keep = slices.DeleteFunc(cfg.Scan.Keep, func(k string) bool { return config.Covers(f, k) })
	}
	cfg.Scan.Folders = folders
	return notes, config.Save(root, cfg)
}

func (a *app) foldersRemoveCmd() *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "remove <folder>...",
		Short: "Stop indexing a folder",
		Long: `Stops indexing a folder. Its documents are dropped from the index on the
next scan, unless --keep is given.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			notes, err := removeFolders(cmd.Context(), root, cwd, args, keep)
			for _, n := range notes {
				fmt.Fprintln(a.stdout, n)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "keep the folder's documents in the index")
	return cmd
}

// removeFolders stops scanning folders; with keep their documents stay.
func removeFolders(ctx context.Context, root, cwd string, args []string, keep bool) ([]string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	folders := cfg.Scan.Folders
	var removed, notes []string
	for _, arg := range args {
		// Accept the stored form even if the folder no longer exists.
		f := filepath.ToSlash(filepath.Clean(arg))
		if nf, err := config.NormalizeFolder(root, cwd, arg); err == nil {
			f = nf
		}
		var ok bool
		if folders, ok = config.RemoveFolder(folders, f); !ok {
			if i := slices.Index(cfg.Scan.Keep, f); i >= 0 && !keep {
				// Removing a kept folder drops its documents for good.
				cfg.Scan.Keep = slices.Delete(cfg.Scan.Keep, i, i+1)
				removed = append(removed, f)
				continue
			}
			return nil, fmt.Errorf("%s is not in the folder list (see \"ragalay folders list\")", arg)
		}
		removed = append(removed, f)
	}
	cfg.Scan.Folders = folders
	if keep {
		for _, f := range removed {
			cfg.Scan.Keep, _ = config.AddFolder(cfg.Scan.Keep, f)
		}
	}
	if err := config.Save(root, cfg); err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		notes = append(notes, "No folders left, so ragalay will index everything in "+root)
	}
	for _, f := range removed {
		if keep {
			notes = append(notes, "removed "+f+" (its documents stay searchable; new files there are ignored)")
		} else {
			notes = append(notes, "removed "+f)
		}
	}
	if keep || len(folders) == 0 {
		return notes, nil
	}
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			for _, f := range removed {
				n, err := store.MarkStaleUnder(ctx, tx, f)
				if err != nil {
					return err
				}
				if n > 0 {
					notes = append(notes, fmt.Sprintf("%d documents from %s will be dropped on the next scan", n, f))
				}
			}
			return nil
		})
	})
	return notes, err
}
