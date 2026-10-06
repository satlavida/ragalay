package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/search"
	"github.com/satlavida/ragalay/internal/setup"
)

// newSearcher builds a Searcher for root. The query model (llama.cpp) is
// loaded when installed; without it search falls back to keywords. The
// Python sidecar is never involved.
func newSearcher(root string, cfg config.Config) (*search.Searcher, func(), error) {
	s := &search.Searcher{Root: root, Cfg: cfg, QueryModel: setup.QueryModelID}
	closeFn := func() {}
	cache, err := setup.CacheDir()
	if err != nil {
		return s, closeFn, nil
	}
	st, err := setup.LoadState(cache)
	if err != nil || !st.QueryReady() {
		return s, closeFn, nil
	}
	q, err := llama.Open(st.LlamaLib, st.QueryModel)
	if err != nil {
		return s, closeFn, nil // keyword fallback; Search reports it
	}
	s.Querier = q
	return s, func() { q.Close() }, nil
}

func (a *app) searchCmd() *cobra.Command {
	var o search.Options
	var modality string
	var groupBy string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search your documents",
		Long: `Finds the passages, pages and images that best match the query.
Agents should use --json: an array of {score, path, kind, modality, page,
heading_path, text, paired_path, parent_path}.`,
		Example: `  ragalay search "how does multi-head attention work"
  ragalay search "invoice total" --path-glob "finance/*" --json
  ragalay search "architecture diagram" --modality image`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			if modality != "" {
				o.Modalities = strings.Split(modality, ",")
			}
			switch groupBy {
			case "", "chunk":
			case "doc":
				o.GroupByDoc = true
			default:
				return fmt.Errorf("--group-by must be chunk or doc")
			}
			s, closeFn, err := newSearcher(root, cfg)
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := s.Search(cmd.Context(), strings.Join(args, " "), o)
			if err != nil {
				if index.IsMismatch(err) {
					return &exitError{ExitModelMismatch, err}
				}
				return err
			}
			if resp.Notice != "" {
				fmt.Fprintln(a.stderr, "note:", resp.Notice)
			}
			if asJSON {
				return a.printJSON(resp.Results)
			}
			a.printResults(resp)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVarP(&o.K, "k", "k", 10, "number of results")
	f.StringVar(&o.Mode, "mode", "hybrid", "hybrid, vector or keyword")
	f.StringVar(&modality, "modality", "", "only these, comma-separated: text, image, pdf_page")
	f.StringVar(&o.PathGlob, "path-glob", "", `only paths matching this pattern, e.g. "papers/*.pdf"`)
	f.StringVar(&groupBy, "group-by", "chunk", "chunk (every passage) or doc (best passage per document)")
	f.IntVar(&o.MaxChars, "max-chars", 2000, "trim result text to this many characters (-1: no limit)")
	f.BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func (a *app) printResults(resp search.Response) {
	w := a.stdout
	if len(resp.Results) == 0 {
		fmt.Fprintln(w, "No results. (Is everything indexed? See \"ragalay status\".)")
		return
	}
	for i, r := range resp.Results {
		where := r.Path
		if r.Page > 0 {
			where += fmt.Sprintf(", page %d", r.Page)
		}
		fmt.Fprintf(w, "%2d. %s  (%.3f)\n", i+1, where, r.Score)
		if r.ParentPath != "" {
			fmt.Fprintf(w, "    image in %s\n", r.ParentPath)
		}
		if r.PairedPath != "" {
			fmt.Fprintf(w, "    transcription: %s\n", r.PairedPath)
		}
		if r.HeadingPath != "" && !strings.HasPrefix(r.HeadingPath, "Page ") {
			fmt.Fprintf(w, "    %s\n", r.HeadingPath)
		}
		if snip := snippet(r.Text, 220); snip != "" {
			fmt.Fprintf(w, "    %s\n", snip)
		}
		fmt.Fprintln(w)
	}
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// scanAndIndex runs one scan plus indexing under the lock (MCP and TUI).
func (a *app) scanAndIndex(ctx context.Context, root string) (scanOutput, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return scanOutput{}, err
	}
	if err := cfg.Validate(); err != nil {
		return scanOutput{}, err
	}
	l, err := lockAcquire(root, "scan")
	if err != nil {
		return scanOutput{}, err
	}
	defer l.Release()
	return runScanIndex(ctx, a, root, cfg)
}
