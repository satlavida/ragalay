// Package search answers queries from the index without Python: query
// vectors come from llama.cpp (or the query cache), vector search is a
// brute-force cosine scan in Turso, keyword search is BM25 over our own
// postings, and the two are merged with Reciprocal Rank Fusion
// (plan1 §4.5).
package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/satlavida/ragalay/internal/bm25"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/store"
)

// Modes.
const (
	Hybrid  = "hybrid"
	Vector  = "vector"
	Keyword = "keyword"
)

// rrfK is the Reciprocal Rank Fusion constant.
const rrfK = 60

// Options control a search.
type Options struct {
	K          int      // results to return (default 10)
	Mode       string   // hybrid (default), vector, keyword
	Modalities []string // text, image, pdf_page; empty = all
	PathGlob   string   // e.g. "papers/*.pdf"; "*" also matches across folders
	GroupByDoc bool     // one result per document
	MaxChars   int      // trim result text (default 2000, <0 = no limit)
}

// Result is one search hit. The JSON form is ragalay's stable search
// output for agents (plan1 §4.5).
type Result struct {
	Score       float64 `json:"score"`
	Path        string  `json:"path"`
	Kind        string  `json:"kind"`
	Modality    string  `json:"modality"`
	Page        int     `json:"page,omitempty"`
	HeadingPath string  `json:"heading_path,omitempty"`
	Text        string  `json:"text,omitempty"`
	PairedPath  string  `json:"paired_path,omitempty"`
	ParentPath  string  `json:"parent_path,omitempty"` // the Markdown file a linked image appears in
	Chunks      int     `json:"chunks,omitempty"`      // with GroupByDoc: matching chunks in the document

	chunkID int64
	docID   int64
	pairID  int64
	pairKey string
}

// Response is a search answer.
type Response struct {
	Query   string   `json:"query"`
	Mode    string   `json:"mode"` // the mode actually used
	Results []Result `json:"results"`
	Notice  string   `json:"notice,omitempty"`
}

// Searcher runs searches for one ragalay folder.
type Searcher struct {
	Root string
	Cfg  config.Config
	// Querier embeds query text (llama.cpp). nil means keyword search only.
	Querier embed.Querier
	// QueryModel names the query model for the cache key.
	QueryModel string
}

// ErrEmptyQuery is returned for a blank query.
var ErrEmptyQuery = errors.New("empty search")

// Search answers query.
func (s *Searcher) Search(ctx context.Context, query string, o Options) (Response, error) {
	query = strings.TrimSpace(query)
	resp := Response{Query: query, Results: []Result{}}
	if query == "" {
		return resp, ErrEmptyQuery
	}
	if o.K <= 0 {
		o.K = 10
	}
	if o.MaxChars == 0 {
		o.MaxChars = 2000
	}
	if o.Mode == "" {
		o.Mode = Hybrid
	}
	if o.Mode != Hybrid && o.Mode != Vector && o.Mode != Keyword {
		return resp, fmt.Errorf("unknown mode %q (use hybrid, vector or keyword)", o.Mode)
	}
	if err := index.CheckSpace(ctx, s.Root, s.Cfg); err != nil {
		return resp, err
	}
	dbPath := filepath.Join(s.Root, config.DirName, config.DBFile)

	var qvec []float32
	if o.Mode != Keyword {
		v, err := s.queryVector(ctx, dbPath, query)
		switch {
		case err == nil:
			qvec = v
		case o.Mode == Vector:
			return resp, err
		default:
			resp.Notice = "keyword search only: " + err.Error()
			o.Mode = Keyword
		}
	}
	resp.Mode = o.Mode

	pool := o.K * 5
	if o.GroupByDoc {
		pool = o.K * 20
	}
	err := store.With(ctx, dbPath, func(db *sql.DB) error {
		var vecHits, kwHits []hit
		var err error
		if qvec != nil {
			if vecHits, err = vectorHits(ctx, db, qvec, o, pool); err != nil {
				return fmt.Errorf("vector search: %w", err)
			}
		}
		if o.Mode != Vector {
			if kwHits, err = keywordHits(ctx, db, query, o, pool); err != nil {
				return fmt.Errorf("keyword search: %w", err)
			}
		}
		ranked := fuse(vecHits, kwHits, o.Mode)
		if resp.Results, err = s.details(ctx, db, ranked); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return resp, err
	}
	resp.Results = mergePairs(resp.Results)
	if o.GroupByDoc {
		resp.Results = groupByDoc(resp.Results)
	}
	if len(resp.Results) > o.K {
		resp.Results = resp.Results[:o.K]
	}
	for i := range resp.Results {
		resp.Results[i].Text = trim(resp.Results[i].Text, o.MaxChars)
		resp.Results[i].Score = math.Round(resp.Results[i].Score*1e4) / 1e4
	}
	return resp, nil
}

// cacheSpace keys the query cache: vector space + query model.
func (s *Searcher) cacheSpace() string { return index.SpaceID(s.Cfg) + "|" + s.QueryModel }

func (s *Searcher) queryVector(ctx context.Context, dbPath, query string) ([]float32, error) {
	norm := store.NormalizeQuery(query)
	var cached []float32
	var hit bool
	err := store.With(ctx, dbPath, func(db *sql.DB) error {
		var err error
		cached, hit, err = store.CachedQuery(ctx, db, s.cacheSpace(), norm)
		return err
	})
	if err == nil && hit && len(cached) == s.Cfg.Embed.Dim {
		return cached, nil
	}
	if s.Querier == nil {
		return nil, errors.New("the search model is not installed (run \"ragalay setup\")")
	}
	raw, err := s.Querier.EmbedQuery(ctx, norm)
	if err != nil {
		return nil, err
	}
	v, err := embed.Fit(raw, s.Cfg.Embed.Dim)
	if err != nil {
		return nil, err
	}
	// The cache is an optimisation: failing to write it is not an error.
	store.With(ctx, dbPath, func(db *sql.DB) error {
		if err := store.PutQuery(ctx, db, s.cacheSpace(), norm, v); err != nil {
			return err
		}
		_, err := store.EvictQueries(ctx, db, s.Cfg.Cache.QueryMax, s.Cfg.Cache.QueryTTL.Duration)
		return err
	})
	return v, nil
}

type hit struct {
	chunkID int64
	score   float64
}

// filterSQL builds the shared WHERE clause for modality and path filters.
func filterSQL(o Options) (string, []any) {
	var where []string
	var args []any
	if len(o.Modalities) > 0 {
		where = append(where, "c.modality IN ("+strings.TrimSuffix(strings.Repeat("?,", len(o.Modalities)), ",")+")")
		for _, m := range o.Modalities {
			args = append(args, m)
		}
	}
	if o.PathGlob != "" {
		where = append(where, "coalesce(c.source_path, d.path) GLOB ?")
		args = append(args, o.PathGlob)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(where, " AND "), args
}

func vectorHits(ctx context.Context, db *sql.DB, q []float32, o Options, limit int) ([]hit, error) {
	filter, args := filterSQL(o)
	rows, err := db.QueryContext(ctx, `SELECT c.id, vector_distance_cos(c.embedding, ?) AS dist
		FROM chunks c JOIN documents d ON d.id = c.document_id
		WHERE c.embedding IS NOT NULL`+filter+`
		ORDER BY dist LIMIT ?`, append(append([]any{embed.Blob(q)}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hit
	for rows.Next() {
		var h hit
		var dist float64
		if err := rows.Scan(&h.chunkID, &dist); err != nil {
			return nil, err
		}
		h.score = 1 - dist // cosine similarity
		out = append(out, h)
	}
	return out, rows.Err()
}

func keywordHits(ctx context.Context, db *sql.DB, query string, o Options, limit int) ([]hit, error) {
	terms := bm25.Counts(query)
	if len(terms) == 0 {
		return nil, nil
	}
	var n int
	var avg sql.NullFloat64
	if err := db.QueryRowContext(ctx, `SELECT count(*), avg(bm25_len) FROM chunks WHERE bm25_len > 0`).Scan(&n, &avg); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	filter, fargs := filterSQL(o)
	scores := map[int64]float64{}
	for term := range terms {
		var termID int64
		var df int
		err := db.QueryRowContext(ctx, `SELECT id, df FROM terms WHERE term = ?`, term).Scan(&termID, &df)
		if errors.Is(err, sql.ErrNoRows) || df <= 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		idf := bm25.IDF(n, df)
		rows, err := db.QueryContext(ctx, `SELECT p.chunk_id, p.tf, c.bm25_len FROM postings p
			JOIN chunks c ON c.id = p.chunk_id JOIN documents d ON d.id = c.document_id
			WHERE p.term_id = ?`+filter, append([]any{termID}, fargs...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var tf, l int
			if err := rows.Scan(&id, &tf, &l); err != nil {
				rows.Close()
				return nil, err
			}
			scores[id] += bm25.Score(idf, tf, l, avg.Float64)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]hit, 0, len(scores))
	for id, s := range scores {
		out = append(out, hit{id, s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].chunkID < out[j].chunkID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// fuse merges ranked lists with RRF in hybrid mode, or passes one list
// through with its native scores.
func fuse(vec, kw []hit, mode string) []hit {
	switch mode {
	case Vector:
		return vec
	case Keyword:
		return kw
	}
	scores := map[int64]float64{}
	for i, h := range vec {
		scores[h.chunkID] += 1 / float64(rrfK+i+1)
	}
	for i, h := range kw {
		scores[h.chunkID] += 1 / float64(rrfK+i+1)
	}
	out := make([]hit, 0, len(scores))
	for id, s := range scores {
		out = append(out, hit{id, s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].chunkID < out[j].chunkID
	})
	return out
}

// details loads what a result shows, keeping the ranking order.
func (s *Searcher) details(ctx context.Context, db *sql.DB, hits []hit) ([]Result, error) {
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		var r Result
		var page sql.NullInt64
		var source, heading, text, pairPath sql.NullString
		err := db.QueryRowContext(ctx, `SELECT c.document_id, c.modality, c.page, c.heading_path, c.text, c.source_path,
			d.path, d.kind, coalesce(d.pair_document_id, 0), p.path
			FROM chunks c JOIN documents d ON d.id = c.document_id
			LEFT JOIN documents p ON p.id = d.pair_document_id
			WHERE c.id = ?`, h.chunkID).Scan(&r.docID, &r.Modality, &page, &heading, &text, &source,
			&r.Path, &r.Kind, &r.pairID, &pairPath)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		r.chunkID, r.Score = h.chunkID, h.score
		r.Page, r.HeadingPath, r.Text, r.PairedPath = int(page.Int64), heading.String, text.String, pairPath.String
		if source.Valid && source.String != "" && source.String != r.Path {
			// A linked image: the hit is the image, shown inside its document.
			r.ParentPath, r.Path, r.Kind = r.Path, source.String, "image"
		}
		if r.pairID != 0 {
			r.pairKey = fmt.Sprint(min(r.docID, r.pairID))
		}
		out = append(out, r)
	}
	return out, nil
}

// mergePairs folds hits from a Markdown transcription and its PDF into one
// result: same page when both know it, otherwise the transcription's hit
// joins the best hit of its PDF (plan1 §5.4). The result cites the PDF page
// and shows the transcription's text.
func mergePairs(rs []Result) []Result {
	out := make([]Result, 0, len(rs))
	page := map[string]int{}       // pairKey#page -> result index (pages known)
	firstPDF := map[string]int{}   // pairKey -> best PDF result
	pagelessMD := map[string]int{} // pairKey -> best transcription hit without a page
	merged := map[int]bool{}       // results that already hold both sides
	for _, r := range rs {
		if r.pairKey == "" || r.ParentPath != "" {
			out = append(out, r)
			continue
		}
		isPDF := r.Kind == "pdf"
		pk := fmt.Sprintf("%s#%d", r.pairKey, r.Page)
		target := -1
		switch {
		case r.Page > 0:
			if i, ok := page[pk]; ok && out[i].docID != r.docID {
				target = i
			} else if i, ok := pagelessMD[r.pairKey]; ok && isPDF && !merged[i] {
				target = i
			}
		case !isPDF: // transcription without page markers
			if i, ok := firstPDF[r.pairKey]; ok && !merged[i] {
				target = i
			}
		}
		if target >= 0 {
			out[target] = combine(out[target], r)
			merged[target] = true
			if out[target].Page > 0 {
				page[fmt.Sprintf("%s#%d", r.pairKey, out[target].Page)] = target
			}
			continue
		}
		i := len(out)
		out = append(out, r)
		if r.Page > 0 {
			page[pk] = i
		}
		if isPDF {
			if _, ok := firstPDF[r.pairKey]; !ok {
				firstPDF[r.pairKey] = i
			}
		} else if r.Page == 0 {
			if _, ok := pagelessMD[r.pairKey]; !ok {
				pagelessMD[r.pairKey] = i
			}
		}
	}
	return out
}

// combine merges b into a (a ranked higher): cite the PDF, show the
// transcription's text.
func combine(a, b Result) Result {
	pdf, md := a, b
	if a.Kind != "pdf" {
		pdf, md = b, a
	}
	m := a
	m.Path, m.Kind, m.PairedPath = pdf.Path, pdf.Kind, md.Path
	if pdf.Page != 0 {
		m.Page = pdf.Page
	}
	if md.Text != "" {
		m.Text, m.HeadingPath, m.Modality = md.Text, md.HeadingPath, md.Modality
	}
	return m
}

// groupByDoc keeps the best hit per document and counts the others.
func groupByDoc(rs []Result) []Result {
	var out []Result
	idx := map[string]int{}
	for _, r := range rs {
		key := r.Path
		if r.ParentPath != "" {
			key = r.ParentPath
		}
		if i, ok := idx[key]; ok {
			out[i].Chunks++
			continue
		}
		r.Chunks = 1
		idx[key] = len(out)
		out = append(out, r)
	}
	return out
}

func trim(s string, n int) string {
	if n < 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n"); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
