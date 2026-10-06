package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/store"
)

// DocInfo is one row of `docs --json`.
type DocInfo struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Chunks     int    `json:"chunks"`
	PairedPath string `json:"paired_path,omitempty"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

func (a *app) docsCmd() *cobra.Command {
	var status, kind string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "List the documents ragalay knows about",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			var docs []store.Document
			err = store.With(cmd.Context(), dbPath(root), func(db *sql.DB) error {
				docs, err = store.ListDocuments(cmd.Context(), db, status, kind)
				return err
			})
			if err != nil {
				return err
			}
			paths := map[int64]string{}
			if status != "" || kind != "" {
				// Pairs may point outside the filter; look them up.
				err = store.With(cmd.Context(), dbPath(root), func(db *sql.DB) error {
					all, err := store.ListDocuments(cmd.Context(), db, "", "")
					for _, d := range all {
						paths[d.ID] = d.Path
					}
					return err
				})
				if err != nil {
					return err
				}
			} else {
				for _, d := range docs {
					paths[d.ID] = d.Path
				}
			}
			out := make([]DocInfo, len(docs))
			for i, d := range docs {
				out[i] = DocInfo{Path: d.Path, Kind: d.Kind, Status: d.Status, Error: d.Error, Chunks: d.ChunkCount,
					PairedPath: paths[d.PairID], Size: d.Size, SHA256: d.SHA256}
			}
			if asJSON {
				return a.printJSON(out)
			}
			if len(out) == 0 {
				fmt.Fprintln(a.stdout, "No documents. Run \"ragalay scan\" to find them.")
				return nil
			}
			for _, d := range out {
				line := fmt.Sprintf("%-10s %-8s %s", d.Status, d.Kind, d.Path)
				if d.PairedPath != "" {
					line += "  <-> " + d.PairedPath
				}
				if d.Error != "" {
					line += "  (" + d.Error + ")"
				}
				fmt.Fprintln(a.stdout, line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only this status: pending, processing, done, failed, stale")
	cmd.Flags().StringVar(&kind, "kind", "", "only this kind: markdown, pdf, image")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func jsonLine(w io.Writer) *json.Encoder { return json.NewEncoder(w) }
