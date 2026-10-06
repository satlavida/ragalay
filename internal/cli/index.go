package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/lock"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/sidecar"
)

// errNotSetUp explains why indexing was skipped.
var errNotSetUp = errors.New(`the AI models are not set up on this computer; run "ragalay setup" to index`)

// indexQueue runs the indexing queue with the real models. The caller holds
// the index lock. quiet suppresses progress output (JSON mode).
func (a *app) indexQueue(ctx context.Context, root string, cfg config.Config, quiet bool) (*index.Summary, error) {
	cache, err := setup.CacheDir()
	if err != nil {
		return nil, err
	}
	st, err := setup.LoadState(cache)
	if err != nil {
		return nil, err
	}
	if !st.Ready() {
		return nil, errNotSetUp
	}
	logs := filepath.Join(root, config.DirName, config.LogsDir)
	logFile, err := os.OpenFile(filepath.Join(logs, "index.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	// The query model's tokenizer counts tokens exactly like the indexing
	// model (shared Qwen3 vocabulary).
	var tok chunk.Tokenizer = chunk.Estimate{}
	if lq, err := llama.Open(st.LlamaLib, st.QueryModel); err == nil {
		defer lq.Close()
		tok = lq
	}
	r := &index.Runner{
		Root: root, Cfg: cfg, Tokenizer: tok, Log: logFile,
		NewEmbedder: func(ctx context.Context) (index.Embedder, error) {
			sideLog, err := os.OpenFile(filepath.Join(logs, "sidecar.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return nil, err
			}
			c, err := sidecar.Start(ctx, sidecar.Options{
				Python: st.Python, ScriptPath: st.Sidecar, Model: cfg.Embed.IndexModel,
				Revision: cfg.Embed.IndexRevision, MaxSide: cfg.Embed.ImageMaxSide, Log: sideLog,
			})
			if err != nil {
				sideLog.Close()
				return nil, err
			}
			return &closingEmbedder{Client: c, log: sideLog}, nil
		},
	}
	var p *progressLine
	if !quiet {
		p = &progressLine{w: a.stdout, tty: isTerminal(a.stdout)}
		r.Progress = p.update
	}
	sum, err := r.Run(ctx)
	if p != nil {
		p.done(sum, err)
	}
	return &sum, err
}

type closingEmbedder struct {
	*sidecar.Client
	log io.Closer
}

func (c *closingEmbedder) Close() error {
	err := c.Client.Close()
	c.log.Close()
	return err
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// progressLine shows indexing progress: one updating line on a terminal,
// a line per document otherwise.
type progressLine struct {
	w      io.Writer
	tty    bool
	shown  bool
	lastAt time.Time
}

func (p *progressLine) update(pr index.Progress) {
	if pr.Current == "" {
		return
	}
	eta := ""
	if pr.ETA > 0 {
		eta = ", about " + humanDuration(pr.ETA) + " left"
	}
	line := fmt.Sprintf("Indexing %d/%d%s: %s", pr.Done+pr.Failed+1, pr.Total, eta, pr.Current)
	if p.tty {
		if len(line) > 100 {
			line = line[:97] + "..."
		}
		fmt.Fprintf(p.w, "\r%-100s", line)
		p.shown = true
		return
	}
	if time.Since(p.lastAt) > 2*time.Second || pr.Done == 0 {
		fmt.Fprintln(p.w, line)
		p.lastAt = time.Now()
	}
}

func (p *progressLine) done(s index.Summary, err error) {
	if p.shown {
		fmt.Fprintf(p.w, "\r%-100s\r", "")
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(p.w, "Stopped. %d documents indexed; the rest continue next time.\n", s.Indexed)
		return
	}
	if s.Indexed+s.Failed+s.Linked == 0 {
		return
	}
	fmt.Fprintf(p.w, "Indexed %d documents (%d chunks) in %s.", s.Indexed+s.Linked, s.Chunks, humanDuration(s.Duration))
	if s.Failed > 0 {
		fmt.Fprintf(p.w, " %d failed:", s.Failed)
		for _, e := range s.Errors {
			fmt.Fprintf(p.w, "\n  %s: %s", e.Path, e.Error)
		}
	}
	fmt.Fprintln(p.w)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (a *app) reembedCmd() *cobra.Command {
	var force, asJSON bool
	cmd := &cobra.Command{
		Use:   "reembed",
		Short: "Rebuild the index after changing the model or its settings",
		Long: `Rebuilds every embedding, for example after changing [embed] dim in
.ragalay/config.toml. Safe to stop: running it (or "ragalay scan") again continues.`,
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
			l, err := lock.Acquire(filepath.Join(root, config.DirName), "reembed")
			if err != nil {
				return lockErr(err)
			}
			defer l.Release()
			queued := int64(0)
			if err := index.CheckSpace(ctx, root, cfg); index.IsMismatch(err) || force {
				if queued, err = index.Reembed(ctx, root, cfg); err != nil {
					return err
				}
				if !asJSON {
					fmt.Fprintf(a.stdout, "Rebuilding the index for %s: %d documents queued.\n", index.SpaceID(cfg), queued)
				}
			} else if err != nil {
				return err
			}
			sum, err := a.indexQueue(ctx, root, cfg, asJSON)
			if asJSON {
				out := map[string]any{"queued": queued, "index": sum}
				if err != nil {
					out["error"] = err.Error()
				}
				a.printJSON(out)
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if errors.Is(err, errNotSetUp) {
				return &exitError{ExitSetupIncomplete, err}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "rebuild even if the settings did not change")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func lockErr(err error) error {
	var held *lock.HeldError
	if errors.As(err, &held) {
		return &exitError{ExitLocked, err}
	}
	return err
}
