package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/search"
)

// liveSearcher searches root with the model that built the live index,
// which differs from the settings while a model switch runs or before
// `ragalay reembed` (plan2 S1). It follows the index: when a switch
// finishes, the next search loads the new query model. The query model
// loads when available; without it search falls back to keywords. The
// Python sidecar is never involved.
type liveSearcher struct {
	root string
	mu   sync.Mutex
	s    *search.Searcher
	key  string // space the loaded query model serves
	done func()
}

func newLiveSearcher(root string) *liveSearcher { return &liveSearcher{root: root, done: func() {}} }

// Search runs a search with the current settings cfg.
func (l *liveSearcher) Search(ctx context.Context, cfg config.Config, q string, o search.Options) (search.Response, error) {
	emb, known, err := index.LiveEmbed(ctx, l.root, cfg)
	if err != nil {
		return search.Response{}, err
	}
	sp, err := index.ReadSpaces(ctx, l.root, cfg)
	if err != nil {
		return search.Response{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if key := fmt.Sprint(emb.SpaceID(), known); l.s == nil || key != l.key {
		l.done()
		l.s, l.done, l.key = &search.Searcher{Root: l.root}, func() {}, key
		if known {
			qr, id, err := newQuerier(emb)
			l.s.QueryModel = id
			if err == nil { // otherwise keyword fallback; Search reports it
				l.s.Querier, l.done = qr, func() { qr.Close() }
			}
		}
	}
	l.s.Cfg = cfg
	l.s.Cfg.Embed = emb
	l.s.Notice = switchNotice(sp, known)
	return l.s.Search(ctx, q, o)
}

// Close unloads the query model.
func (l *liveSearcher) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done()
	l.s, l.done = nil, func() {}
}

// switchNotice explains results that come from another model than the
// settings name.
func switchNotice(sp index.Spaces, known bool) string {
	switch {
	case sp.Shadow != nil:
		return fmt.Sprintf("switching models (%d of %d documents done): results use the previous model until it finishes",
			sp.Shadow.Done, sp.Shadow.Total)
	case sp.Mismatch() && known:
		return `the settings name a different model: results use the previous one until you run "ragalay reembed"`
	case sp.Mismatch():
		return `the settings name a different model: keyword search only until you run "ragalay reembed"`
	}
	return ""
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
			s := newLiveSearcher(root)
			defer s.Close()
			resp, err := s.Search(cmd.Context(), cfg, strings.Join(args, " "), o)
			if err != nil {
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
