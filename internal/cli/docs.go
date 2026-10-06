package cli

import (
	"context"
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
			out, err := docInfos(cmd.Context(), root, status, kind)
			if err != nil {
				return err
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

// docInfos lists documents with their paired paths (also used by MCP).
func docInfos(ctx context.Context, root, status, kind string) ([]DocInfo, error) {
	var docs, all []store.Document
	err := store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var err error
		if all, err = store.ListDocuments(ctx, db, "", ""); err != nil {
			return err
		}
		docs, err = store.ListDocuments(ctx, db, status, kind)
		return err
	})
	if err != nil {
		return nil, err
	}
	paths := make(map[int64]string, len(all))
	for _, d := range all {
		paths[d.ID] = d.Path
	}
	out := make([]DocInfo, len(docs))
	for i, d := range docs {
		out[i] = DocInfo{Path: d.Path, Kind: d.Kind, Status: d.Status, Error: d.Error, Chunks: d.ChunkCount,
			PairedPath: paths[d.PairID], Size: d.Size, SHA256: d.SHA256}
	}
	return out, nil
}
