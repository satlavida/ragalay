package cli

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/store"
)

const jinaLicenseText = `This folder uses two AI models made by Jina AI:
  - jina-embeddings-v5-omni-small-retrieval  (indexes your files)
  - jina-embeddings-v5-text-small-retrieval  (understands your searches)

Both are licensed CC BY-NC 4.0: free for personal and research use,
NOT for commercial use. Details: https://creativecommons.org/licenses/by-nc/4.0/
ragalay itself is Apache-2.0. Nothing you index leaves your computer.`

const gemmaLicenseText = `This folder uses EmbeddingGemma 2, an AI model made by Google, to index
your files and understand your searches.

It is licensed Apache 2.0: free for any use, including commercial.
ragalay itself is Apache-2.0. Nothing you index leaves your computer.`

// licenseText is what a profile's setup shows before downloading.
func licenseText(profile string) string {
	if profile == embed.JinaV5 {
		return jinaLicenseText
	}
	return gemmaLicenseText
}

func (a *app) setupCmd() *cobra.Command {
	var device string
	var acceptLicense, yes bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Download and check the AI models ragalay needs (one time per computer)",
		Long: `Downloads Python, PyTorch, the indexing model and the search model into a
shared folder on this computer (every ragalay folder reuses it), then runs a
self-test. Safe to run again: finished steps are skipped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
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
			cache, err := setup.CacheDir()
			if err != nil {
				return err
			}
			prof, err := cfg.Embed.Lookup()
			if err != nil {
				return err
			}
			if !prof.Local() {
				fmt.Fprintf(a.stdout, "This folder uses %s at %s; there is nothing to install.\n",
					cfg.Embed.OpenAI.Model, cfg.Embed.OpenAI.Host())
				return nil
			}
			opts := setup.Options{Device: device, Profile: prof.Name, MaxSide: cfg.Embed.ImageMaxSide}
			st, err := setup.LoadState(cache)
			if err != nil {
				return err
			}
			plan, err := setup.Prepare(ctx, cache, opts)
			if err != nil {
				return err
			}

			w := a.stdout
			if !prof.NonCommercial && len(plan.Downloads) > 0 {
				fmt.Fprintln(w, licenseText(prof.Name))
				fmt.Fprintln(w)
			}
			if prof.NonCommercial && st.Profile(prof.Name).LicenseAccepted == "" {
				fmt.Fprintln(w, licenseText(prof.Name))
				fmt.Fprintln(w)
				if !acceptLicense {
					ok, err := a.confirm("Do you accept these terms? Type yes to continue: ", true)
					if err != nil {
						return err
					}
					if !ok {
						return &exitError{ExitSetupIncomplete, errors.New("setup needs the model license accepted (or pass --accept-license)")}
					}
				}
				st.AcceptLicense(prof.Name)
				if err := st.Save(cache); err != nil {
					return err
				}
			}

			fmt.Fprintf(w, "Accelerator: %s (%s)\n", plan.Detection.Variant, plan.Detection.Reason)
			fmt.Fprintf(w, "Shared folder: %s\n", cache)
			if len(plan.Downloads) > 0 {
				fmt.Fprintln(w, "To download:")
				for _, d := range plan.Downloads {
					fmt.Fprintf(w, "  %-48s ~%s\n", d.Name, humanBytes(d.Size))
				}
				fmt.Fprintf(w, "  %-48s ~%s\n", "total", humanBytes(plan.Total))
				if !yes {
					ok, err := a.confirm("Continue? [Y/n] ", false)
					if err != nil {
						return err
					}
					if !ok {
						return &exitError{ExitSetupIncomplete, errors.New("setup cancelled")}
					}
				}
			}

			logFile, err := os.OpenFile(filepath.Join(root, config.DirName, config.LogsDir, "setup.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer logFile.Close()
			fmt.Fprintf(logFile, "\n==== ragalay %s setup %s ====\n", Version, time.Now().Format(time.RFC3339))
			opts.Log = logFile
			opts.Report = &termReporter{w: w}

			st, err = setup.Run(ctx, cache, opts)
			opts.Report.(*termReporter).endLine()
			if err != nil {
				return &exitError{ExitSetupIncomplete, fmt.Errorf("%w\n(details in .ragalay/logs/setup.log)", err)}
			}
			if err := recordSetup(ctx, root, cfg, st); err != nil {
				return err
			}
			fmt.Fprintf(w, "\nSetup complete. Indexing runs on %s; search runs on the CPU.\n", st.DeviceName)
			return nil
		},
	}
	cmd.Flags().StringVar(&device, "device", "auto", "indexing accelerator: auto, cpu, cuda, mps, rocm-gfx1201, rocm-gfx1200")
	cmd.Flags().BoolVar(&acceptLicense, "accept-license", false, "accept a non-commercial model license (Jina v5) without asking")
	cmd.Flags().BoolVar(&yes, "yes", false, "download without asking")
	return cmd
}

// recordSetup stores the license acceptance and the index's vector space in
// this folder's database. An existing embed_id is never overwritten: a
// mismatch is handled by re-embedding (plan1 §4.6).
func recordSetup(ctx context.Context, root string, cfg config.Config, st setup.State) error {
	return store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			if at := st.Profile(cfg.Embed.Profile).LicenseAccepted; at != "" {
				if err := store.SetMeta(ctx, tx, "license_accepted_at", at); err != nil {
					return err
				}
			}
			cur, err := store.Meta(ctx, tx, "embed_id")
			if err != nil {
				return err
			}
			if cur == "" {
				if err := store.SetLiveSpace(ctx, tx, cfg.Embed.SpaceID(), cfg.Embed.Dim, index.EmbedJSON(cfg.Embed)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// confirm asks a yes/no question. Without a terminal it refuses, so scripts
// and agents must pass the flags explicitly.
func (a *app) confirm(prompt string, strictYes bool) (bool, error) {
	if !a.interactive() {
		return false, &exitError{ExitSetupIncomplete,
			errors.New("no terminal to ask in; run \"ragalay setup --accept-license --yes\"")}
	}
	fmt.Fprint(a.stdout, prompt)
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if errors.Is(err, io.EOF) {
		fmt.Fprintln(a.stdout)
		return false, nil // input closed: never treat that as consent
	}
	if err != nil {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if strictYes {
		return ans == "yes", nil
	}
	return ans == "" || ans == "y" || ans == "yes", nil
}

func (a *app) interactive() bool {
	if a.isTTY != nil {
		return a.isTTY()
	}
	// Not os.ModeCharDevice: Windows reports NUL as a character device.
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// termReporter prints setup progress for a terminal.
type termReporter struct {
	w        io.Writer
	inLine   bool
	lastDraw time.Time
}

func (t *termReporter) endLine() {
	if t.inLine {
		fmt.Fprintln(t.w)
		t.inLine = false
	}
}

func (t *termReporter) Step(n, total int, title string) {
	t.endLine()
	fmt.Fprintf(t.w, "\n[%d/%d] %s\n", n, total, title)
}

func (t *termReporter) Note(msg string) {
	t.endLine()
	fmt.Fprintln(t.w, "      "+msg)
}

func (t *termReporter) Progress(done, total int64) {
	if time.Since(t.lastDraw) < 250*time.Millisecond && done != total {
		return
	}
	t.lastDraw = time.Now()
	if total > 0 {
		pct := min(100, done*100/total)
		fmt.Fprintf(t.w, "\r      %s / %s (%d%%)   ", humanBytes(done), humanBytes(total), pct)
	} else {
		fmt.Fprintf(t.w, "\r      %s   ", humanBytes(done))
	}
	t.inLine = true
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
