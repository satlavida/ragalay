package cli

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/lock"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/store"
)

// StatusReport is the stable `status --json` schema.
type StatusReport struct {
	Root          string       `json:"root"`
	Version       string       `json:"version"`
	ConfigErrors  []string     `json:"config_errors"`
	Folders       []FolderInfo `json:"folders"`
	WholeDir      bool         `json:"whole_directory"`
	SetupComplete bool         `json:"setup_complete"`
	Setup         SetupInfo    `json:"setup"`
	Index         store.Stats  `json:"index"`
	Queued        int          `json:"queued"`   // documents waiting to be indexed
	Pairs         int          `json:"pairs"`    // Markdown ↔ PDF links
	Indexing      *lock.Info   `json:"indexing"` // the process holding the index lock, if any
}

// SetupInfo is the machine-wide setup state (shared cache).
type SetupInfo struct {
	Ready       bool   `json:"ready"`        // indexing and search both work
	SearchReady bool   `json:"search_ready"` // query model installed
	Device      string `json:"device,omitempty"`
	DeviceName  string `json:"device_name,omitempty"`
	CacheDir    string `json:"cache_dir"`
}

func (a *app) statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what ragalay has indexed",
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
			rep := StatusReport{Root: root, Version: Version, ConfigErrors: []string{}, WholeDir: len(cfg.Scan.Folders) == 0}
			if verr := cfg.Validate(); verr != nil {
				rep.ConfigErrors = splitErrors(verr)
			}
			if rep.Folders, err = folderInfos(cmd.Context(), root, cfg); err != nil {
				return err
			}
			err = store.With(cmd.Context(), dbPath(root), func(db *sql.DB) error {
				if rep.Index, err = store.ReadStats(cmd.Context(), db); err != nil {
					return err
				}
				if rep.Queued, err = store.QueuedJobs(cmd.Context(), db); err != nil {
					return err
				}
				return db.QueryRowContext(cmd.Context(),
					`SELECT count(*) FROM documents WHERE pair_document_id IS NOT NULL AND kind = 'markdown'`).Scan(&rep.Pairs)
			})
			if info, alive := lock.Read(filepath.Join(root, config.DirName)); alive {
				rep.Indexing = &info
			}
			if err != nil {
				return err
			}
			if cache, err := setup.CacheDir(); err == nil {
				st, _ := setup.LoadState(cache)
				rep.Setup = SetupInfo{Ready: st.Ready(), SearchReady: st.QueryReady(),
					Device: st.Device, DeviceName: st.DeviceName, CacheDir: cache}
			}
			// Setup is machine-wide: a new folder on a set-up computer is ready too.
			rep.SetupComplete = rep.Setup.Ready
			if asJSON {
				return a.printJSON(rep)
			}
			a.printStatus(rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func (a *app) printStatus(r StatusReport) {
	w := a.stdout
	fmt.Fprintf(w, "ragalay %s in %s\n\n", r.Version, r.Root)
	if len(r.ConfigErrors) > 0 {
		fmt.Fprintln(w, "Problems in .ragalay/config.toml:")
		for _, e := range r.ConfigErrors {
			fmt.Fprintln(w, "  -", e)
		}
		fmt.Fprintln(w)
	}
	if r.WholeDir {
		fmt.Fprintln(w, "Folders: everything in this directory")
	} else {
		fmt.Fprintln(w, "Folders:")
	}
	for _, f := range r.Folders {
		note := ""
		if !f.Exists {
			note = "  (missing!)"
		}
		fmt.Fprintf(w, "  %-30s %6d documents%s\n", f.Path, f.Documents, note)
	}
	fmt.Fprintf(w, "\nDocuments: %d", r.Index.Documents)
	if r.Index.Documents > 0 {
		keys := make([]string, 0, len(r.Index.ByStatus))
		for k := range r.Index.ByStatus {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		fmt.Fprint(w, " (")
		for i, k := range keys {
			if i > 0 {
				fmt.Fprint(w, ", ")
			}
			fmt.Fprintf(w, "%d %s", r.Index.ByStatus[k], k)
		}
		fmt.Fprint(w, ")")
	}
	fmt.Fprintf(w, "\nChunks:    %d\n", r.Index.Chunks)
	if r.Pairs > 0 {
		fmt.Fprintf(w, "Pairs:     %d Markdown transcriptions linked to their PDFs\n", r.Pairs)
	}
	if r.Queued > 0 {
		fmt.Fprintf(w, "Queue:     %d documents waiting to be indexed\n", r.Queued)
	}
	if r.Indexing != nil {
		fmt.Fprintf(w, "Indexing:  running (%q, pid %d, since %s)\n", r.Indexing.Command, r.Indexing.PID,
			r.Indexing.Started.Local().Format("15:04:05"))
	}
	if r.SetupComplete {
		fmt.Fprintf(w, "\nModels:    ready (indexing on %s)\n", r.Setup.DeviceName)
	} else {
		fmt.Fprintln(w, "\nModels:    not set up yet. Run \"ragalay setup\".")
	}
}

func splitErrors(err error) []string {
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		var out []string
		for _, e := range u.Unwrap() {
			out = append(out, e.Error())
		}
		return out
	}
	return []string{err.Error()}
}
