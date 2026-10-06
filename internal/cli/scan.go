package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/lock"
	"github.com/satlavida/ragalay/internal/scan"
)

func (a *app) scanCmd() *cobra.Command {
	var watch, asJSON bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Find new, changed, moved and deleted files",
		Long: `Looks through the indexed folders and updates ragalay's list of documents.
With --watch it keeps running and rescans whenever files change (Ctrl+C stops).
Only one ragalay process can scan or index a folder at a time.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("fix .ragalay/config.toml first:\n%w", err)
			}
			name := "scan"
			if watch {
				name = "scan --watch"
			}
			l, err := lock.Acquire(filepath.Join(root, config.DirName), name)
			var held *lock.HeldError
			if errors.As(err, &held) {
				return &exitError{ExitLocked, err}
			}
			if err != nil {
				return err
			}
			defer l.Release()

			if !watch {
				rep, err := scan.Run(cmd.Context(), root, cfg)
				if err != nil {
					return err
				}
				if asJSON {
					return a.printJSON(rep)
				}
				a.printScan(rep, false)
				return nil
			}

			if !asJSON {
				fmt.Fprintln(a.stdout, "Watching for changes. Press Ctrl+C to stop.")
			}
			first := true
			err = scan.Watch(cmd.Context(), root, 2*time.Second, func(rep scan.Report, err error) {
				shown := first
				first = false
				switch {
				case err != nil && asJSON:
					a.printJSONLine(map[string]any{"time": time.Now(), "error": err.Error()})
				case err != nil:
					fmt.Fprintf(a.stderr, "%s  error: %v\n", time.Now().Format("15:04:05"), err)
				case asJSON:
					a.printJSONLine(map[string]any{"time": time.Now(), "scan": rep})
				case shown || rep.Changes() || len(rep.Warnings) > 0:
					a.printScan(rep, true) // always show the first scan so people see it working
				}
			})
			if cmd.Context().Err() != nil {
				return nil // Ctrl+C
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "keep running and rescan when files change")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output (one JSON object per scan with --watch)")
	return cmd
}

func (a *app) printScan(r scan.Report, stamped bool) {
	w := a.stdout
	prefix := ""
	if stamped {
		prefix = time.Now().Format("15:04:05") + "  "
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(len(r.New), "new")
	add(len(r.Changed), "changed")
	add(len(r.Moved), "moved")
	add(len(r.Deleted), "removed")
	add(len(r.Revived), "back in scope")
	if len(parts) == 0 {
		parts = []string{"no changes"}
	}
	fmt.Fprintf(w, "%sScanned %d files: %s.\n", prefix, r.Found, strings.Join(parts, ", "))
	list := func(label string, items []string) {
		for _, it := range items {
			fmt.Fprintf(w, "%s  %-8s %s\n", prefix, label, it)
		}
	}
	list("new", r.New)
	list("changed", r.Changed)
	for _, m := range r.Moved {
		fmt.Fprintf(w, "%s  %-8s %s -> %s\n", prefix, "moved", m.From, m.To)
	}
	list("removed", r.Deleted)
	list("back", r.Revived)
	if r.Recovered > 0 {
		fmt.Fprintf(w, "%s  Resumed %d documents left unfinished last time.\n", prefix, r.Recovered)
	}
	for _, f := range r.Missing {
		fmt.Fprintf(w, "%s  warning: folder %s does not exist\n", prefix, f)
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "%s  warning: %s\n", prefix, warn)
	}
	if r.Queued > 0 {
		fmt.Fprintf(w, "%s%d documents are waiting to be indexed.\n", prefix, r.Queued)
	}
}

func (a *app) printJSONLine(v any) {
	enc := jsonLine(a.stdout)
	enc.Encode(v)
}
