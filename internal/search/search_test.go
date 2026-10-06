package search

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/satlavida/ragalay/internal/bm25"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/store"
)

// bow is a bag-of-words embedding: cosine similarity tracks word overlap,
// which is enough to test ranking without the real model.
func bow(text string) []float32 {
	v := make([]float32, 1024)
	for _, w := range bm25.Tokenize(text) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%1024]++
	}
	v[1023] += 0.01 // never a zero vector
	return v
}

type bowEmbedder struct{}

func (bowEmbedder) EmbedDocuments(_ context.Context, in []embed.Input) ([][]float32, error) {
	out := make([][]float32, len(in))
	for i, x := range in {
		t := x.Text
		if x.Modality != embed.Text {
			t += " " + filepath.Base(x.Path) + fmt.Sprintf(" page%d", x.Page)
		}
		out[i] = bow(t)
	}
	return out, nil
}
func (bowEmbedder) Close() error { return nil }

type bowQuerier struct{ calls atomic.Int32 }

func (q *bowQuerier) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	q.calls.Add(1)
	return bow(text), nil
}
func (q *bowQuerier) Close() error { return nil }

func put(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pdfBytes(pages ...string) []byte {
	var objs []string
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs, "<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, text := range pages {
		content := fmt.Sprintf("BT /F1 12 Tf 40 700 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj %s endobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return []byte(b.String())
}

func pngBytes() []byte {
	var b strings.Builder
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	return []byte(b.String())
}

// indexedRoot builds and indexes a small library.
func indexedRoot(t *testing.T) (string, config.Config) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, config.DirName), 0o755)
	cfg := config.Default()
	config.Save(root, cfg)
	ctx := context.Background()
	store.With(ctx, filepath.Join(root, config.DirName, config.DBFile), func(db *sql.DB) error { return store.Migrate(ctx, db, 1024) })

	put(t, root, "notes/transformer.md", []byte("# Transformer\n\nThe transformer uses multi-head self-attention instead of recurrence.\n\n![attention heads diagram](img/heads.png)"))
	put(t, root, "notes/img/heads.png", pngBytes())
	put(t, root, "notes/bread.md", []byte("# Sourdough\n\nSourdough bread needs a starter, flour, water and salt. Bake at high heat."))
	put(t, root, "papers/bert.pdf", pdfBytes("BERT pre-training of deep bidirectional transformers", "Masked language model masks 15 percent of tokens"))
	// A transcription paired with its PDF by name.
	put(t, root, "transcripts/budget.md", []byte("# Budget\n\nQuarterly revenue grew by twelve percent in the budget report."))
	put(t, root, "finance/budget.pdf", pdfBytes("Quarterly revenue grew by twelve percent in the budget report."))
	put(t, root, "photos/sunset.png", pngBytes())

	if _, err := scan.Run(ctx, root, cfg); err != nil {
		t.Fatal(err)
	}
	r := &index.Runner{Root: root, Cfg: cfg, NewEmbedder: func(context.Context) (index.Embedder, error) { return bowEmbedder{}, nil }}
	if sum, err := r.Run(ctx); err != nil || sum.Failed != 0 {
		t.Fatalf("index: %+v %v", sum, err)
	}
	return root, cfg
}

func TestSearchModes(t *testing.T) {
	root, cfg := indexedRoot(t)
	q := &bowQuerier{}
	s := &Searcher{Root: root, Cfg: cfg, Querier: q, QueryModel: "bow"}
	ctx := context.Background()
	for _, mode := range []string{Hybrid, Vector, Keyword} {
		resp, err := s.Search(ctx, "multi-head self-attention transformer", Options{Mode: mode})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if resp.Mode != mode || len(resp.Results) == 0 || resp.Results[0].Path != "notes/transformer.md" {
			t.Errorf("%s: top result %+v", mode, first(resp))
		}
	}
	resp, _ := s.Search(ctx, "sourdough bread starter", Options{})
	if r := first(resp); r.Path != "notes/bread.md" || r.HeadingPath != "Sourdough" || !strings.Contains(r.Text, "starter") {
		t.Errorf("bread query: %+v", r)
	}
	// Repeating a query uses the cache, not the query model.
	before := q.calls.Load()
	s.Search(ctx, "  sourdough   bread starter ", Options{})
	if q.calls.Load() != before {
		t.Error("repeated query was embedded again instead of using the cache")
	}
}

func TestSearchFiltersAndCitations(t *testing.T) {
	root, cfg := indexedRoot(t)
	s := &Searcher{Root: root, Cfg: cfg, Querier: &bowQuerier{}, QueryModel: "bow"}
	ctx := context.Background()

	resp, err := s.Search(ctx, "masked language model tokens", Options{PathGlob: "papers/*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		if !strings.HasPrefix(r.Path, "papers/") {
			t.Errorf("path glob leaked %s", r.Path)
		}
	}
	if r := first(resp); r.Path != "papers/bert.pdf" || r.Page != 2 {
		t.Errorf("PDF hit must cite its page: %+v", r)
	}

	resp, _ = s.Search(ctx, "heads diagram", Options{Modalities: []string{embed.Image}})
	for _, r := range resp.Results {
		if r.Modality != embed.Image {
			t.Errorf("modality filter leaked %+v", r)
		}
	}
	if r := first(resp); r.Path != "notes/img/heads.png" || r.ParentPath != "notes/transformer.md" {
		t.Errorf("linked image hit should name its document: %+v", r)
	}

	resp, _ = s.Search(ctx, "BERT transformers", Options{MaxChars: 10, Mode: Keyword})
	if r := first(resp); len([]rune(r.Text)) > 11 || !strings.HasSuffix(r.Text, "…") {
		t.Errorf("text not trimmed: %q", r.Text)
	}
}

func TestPairedHitsMerge(t *testing.T) {
	root, cfg := indexedRoot(t)
	s := &Searcher{Root: root, Cfg: cfg, Querier: &bowQuerier{}, QueryModel: "bow"}
	resp, err := s.Search(context.Background(), "quarterly revenue budget", Options{})
	if err != nil {
		t.Fatal(err)
	}
	var budget []Result
	for _, r := range resp.Results {
		if strings.Contains(r.Path, "budget") || strings.Contains(r.PairedPath, "budget") {
			budget = append(budget, r)
		}
	}
	if len(budget) == 0 || budget[0].Path != "finance/budget.pdf" || budget[0].PairedPath != "transcripts/budget.md" ||
		budget[0].Page != 1 || !strings.Contains(budget[0].Text, "twelve percent") {
		t.Fatalf("pair should merge into one result citing the PDF page with the transcription text: %+v", budget)
	}
	for _, r := range budget[1:] {
		if r.Path == "transcripts/budget.md" && r.PairedPath == "finance/budget.pdf" && r.Page == 0 {
			t.Errorf("transcription hit not merged: %+v", r)
		}
	}
}

func TestGroupByDoc(t *testing.T) {
	root, cfg := indexedRoot(t)
	s := &Searcher{Root: root, Cfg: cfg, Querier: &bowQuerier{}, QueryModel: "bow"}
	resp, err := s.Search(context.Background(), "BERT masked language model transformers", Options{GroupByDoc: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range resp.Results {
		key := r.Path + "|" + r.ParentPath
		if seen[key] {
			t.Errorf("document %s appears twice", key)
		}
		seen[key] = true
	}
	if r := first(resp); r.Path != "papers/bert.pdf" || r.Chunks < 2 {
		t.Errorf("grouped top result: %+v", r)
	}
}

func TestKeywordFallbackWithoutQueryModel(t *testing.T) {
	root, cfg := indexedRoot(t)
	s := &Searcher{Root: root, Cfg: cfg} // no llama.cpp
	resp, err := s.Search(context.Background(), "sourdough", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Mode != Keyword || !strings.Contains(resp.Notice, "keyword search only") || first(resp).Path != "notes/bread.md" {
		t.Fatalf("fallback: %+v", resp)
	}
	if _, err := s.Search(context.Background(), "sourdough", Options{Mode: Vector}); err == nil {
		t.Fatal("vector mode without a query model must fail")
	}
}

func TestSearchRefusesOnModelMismatch(t *testing.T) {
	root, cfg := indexedRoot(t)
	cfg.Embed.Dim = 256
	s := &Searcher{Root: root, Cfg: cfg, Querier: &bowQuerier{}}
	if _, err := s.Search(context.Background(), "anything", Options{}); !index.IsMismatch(err) {
		t.Fatalf("want model mismatch, got %v", err)
	}
	if _, err := s.Search(context.Background(), "  ", Options{}); err != ErrEmptyQuery {
		t.Fatalf("empty query: %v", err)
	}
}

// TestSearchNeverStartsPython: search must work without the indexing
// sidecar (plan1 G2), so it must not even depend on it.
func TestSearchNeverStartsPython(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/satlavida/ragalay/internal/search").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/sidecar") {
			t.Fatal("internal/search depends on the Python sidecar")
		}
	}
}

func first(r Response) Result {
	if len(r.Results) == 0 {
		return Result{}
	}
	return r.Results[0]
}
