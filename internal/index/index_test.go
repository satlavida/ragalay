package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/store"
)

// fakeEmbedder returns deterministic 1024-dim vectors derived from the
// input, so equal inputs get equal vectors.
type fakeEmbedder struct {
	calls  atomic.Int32
	block  chan struct{} // if set, the second call waits for it or ctx
	closed atomic.Bool
}

func (f *fakeEmbedder) EmbedDocuments(ctx context.Context, in []embed.Input) ([][]float32, error) {
	if f.calls.Add(1) == 2 && f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := make([][]float32, len(in))
	for i, x := range in {
		h := sha256.Sum256([]byte(x.Modality + "|" + x.Text + "|" + x.Path + fmt.Sprint(x.Page)))
		v := make([]float32, 1024)
		for j := range v {
			v[j] = float32(h[j%32]) - 127
		}
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) Close() error { f.closed.Store(true); return nil }

func put(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b strings.Builder
	png.Encode(&b, img)
	return []byte(b.String())
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
		content := ""
		if text != "" {
			content = fmt.Sprintf("BT /F1 12 Tf 40 700 Td (%s) Tj ET", text)
		}
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

// mixedRoot is plan1 Phase 5's "mixed sample folder".
func mixedRoot(t *testing.T) (string, config.Config) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, config.DirName), 0o755)
	cfg := config.Default()
	cfg.Embed, _ = cfg.Embed.UseProfile(embed.JinaV5, 1024) // the fake embedders are 1024-dim
	cfg.Pairs = []config.Pair{{MD: "md", PDF: "pdf"}}
	config.Save(root, cfg)
	ctx := context.Background()
	if err := store.With(ctx, dbPath(root), func(db *sql.DB) error { return store.Migrate(ctx, db, 1024) }); err != nil {
		t.Fatal(err)
	}
	put(t, root, "notes/guide.md", []byte("# Guide\n\nMulti-head attention explained.\n\n![Diagram](img/diagram.png)\n\n## More\n\nPositional encoding uses sine waves."))
	put(t, root, "notes/img/diagram.png", pngBytes(t))
	put(t, root, "photos/cat.png", pngBytes(t))
	put(t, root, "papers/attention.pdf", pdfBytes("Attention Is All You Need", "BLEU 28.4 results"))
	put(t, root, "papers/scan.pdf", pdfBytes(""))
	put(t, root, "md/report.md", []byte("# Report\n\nQuarterly revenue grew."))
	put(t, root, "pdf/report.pdf", pdfBytes("Quarterly revenue grew (original)."))
	put(t, root, "papers/broken.pdf", []byte("not a pdf"))
	if _, err := scan.Run(ctx, root, cfg); err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

func docsByPath(t *testing.T, root string) map[string]store.Document {
	t.Helper()
	out := map[string]store.Document{}
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		ds, err := store.ListDocuments(ctx, db, "", "")
		for _, d := range ds {
			out[d.Path] = d
		}
		return err
	})
	return out
}

func runner(root string, cfg config.Config, f *fakeEmbedder) *Runner {
	return &Runner{Root: root, Cfg: cfg, NewEmbedder: func(context.Context) (Embedder, error) { return f, nil }}
}

func TestMixedFolderIndexesEndToEnd(t *testing.T) {
	root, cfg := mixedRoot(t)
	f := &fakeEmbedder{}
	var progress []Progress
	r := runner(root, cfg, f)
	r.Progress = func(p Progress) { progress = append(progress, p) }
	sum, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Indexed != 6 || sum.Linked != 1 || sum.Failed != 1 || !f.closed.Load() {
		t.Fatalf("summary: %+v", sum)
	}
	d := docsByPath(t, root)
	for path, want := range map[string]struct {
		status string
		chunks int
	}{
		"notes/guide.md":        {store.StatusDone, 2}, // 1 text chunk (small sections merge) + 1 linked image
		"notes/img/diagram.png": {store.StatusDone, 0}, // indexed with guide.md
		"photos/cat.png":        {store.StatusDone, 1},
		"papers/attention.pdf":  {store.StatusDone, 4}, // 2 text + 2 page images
		"papers/scan.pdf":       {store.StatusDone, 1}, // page image only
		"md/report.md":          {store.StatusDone, 1},
		"pdf/report.pdf":        {store.StatusDone, 1}, // paired: page image only, text from the transcription
		"papers/broken.pdf":     {store.StatusFailed, 0},
	} {
		got := d[path]
		if got.Status != want.status || got.ChunkCount != want.chunks {
			t.Errorf("%s: status %s chunks %d, want %s %d (%s)", path, got.Status, got.ChunkCount, want.status, want.chunks, got.Error)
		}
	}
	if d["notes/img/diagram.png"].Error != store.LinkedNote || !strings.Contains(d["pdf/report.pdf"].Error, "transcription") {
		t.Errorf("notes: %q / %q", d["notes/img/diagram.png"].Error, d["pdf/report.pdf"].Error)
	}
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var src string
		db.QueryRowContext(ctx, `SELECT source_path FROM chunks WHERE modality = 'image' AND document_id = ?`, d["notes/guide.md"].ID).Scan(&src)
		if src != "notes/img/diagram.png" {
			t.Errorf("linked image chunk source = %q", src)
		}
		var postings int
		db.QueryRowContext(ctx, `SELECT count(*) FROM postings p JOIN terms t ON t.id = p.term_id WHERE t.term = 'attention'`).Scan(&postings)
		if postings < 2 {
			t.Errorf("BM25 postings for 'attention' = %d", postings)
		}
		if id, _ := store.Meta(ctx, db, "embed_id"); id != SpaceID(cfg) {
			t.Errorf("embed_id = %q", id)
		}
		return nil
	})
	if len(progress) == 0 || progress[len(progress)-1].Done != 7 {
		t.Errorf("progress: %+v", progress)
	}

	// The broken PDF is retried on the next runs, up to MaxAttempts.
	for i := 1; i < store.MaxAttempts; i++ {
		sum, _ := runner(root, cfg, &fakeEmbedder{}).Run(context.Background())
		if sum.Failed != 1 {
			t.Fatalf("retry %d: %+v", i, sum)
		}
	}
	g := &fakeEmbedder{}
	sum, _ = runner(root, cfg, g).Run(context.Background())
	if sum.Failed != 0 || g.calls.Load() != 0 {
		t.Fatalf("after %d attempts the broken PDF must not be retried: %+v", store.MaxAttempts, sum)
	}
}

func TestEmptyQueueNeverStartsTheModel(t *testing.T) {
	root, cfg := mixedRoot(t)
	runner(root, cfg, &fakeEmbedder{}).Run(context.Background())
	started := false
	r := &Runner{Root: root, Cfg: cfg, NewEmbedder: func(context.Context) (Embedder, error) {
		started = true
		return &fakeEmbedder{}, nil
	}}
	// Only the failed PDF is retried; it fails in extraction, before embedding.
	if _, err := r.Run(context.Background()); err != nil || started {
		t.Fatalf("model started for no embedding work: started=%v err=%v", started, err)
	}
}

func TestInterruptedRunResumes(t *testing.T) {
	root, cfg := mixedRoot(t)
	f := &fakeEmbedder{block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for f.calls.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel() // killed while embedding the second document
	}()
	sum, err := runner(root, cfg, f).Run(ctx)
	if !errors.Is(err, context.Canceled) || sum.Indexed != 1 {
		t.Fatalf("interrupted run: %+v %v", sum, err)
	}
	d := docsByPath(t, root)
	processing := 0
	for _, doc := range d {
		if doc.Status == store.StatusProcessing {
			processing++
		}
	}
	if processing != 0 {
		t.Fatalf("%d documents left in processing", processing)
	}
	sum, err = runner(root, cfg, &fakeEmbedder{}).Run(context.Background())
	if err != nil || sum.Indexed+sum.Linked+sum.Failed != 7 {
		t.Fatalf("resumed run: %+v %v", sum, err)
	}
}

func TestDimChangeNeedsReembed(t *testing.T) {
	root, cfg := mixedRoot(t)
	runner(root, cfg, &fakeEmbedder{}).Run(context.Background())

	cfg.Embed.Dim = 512
	_, err := runner(root, cfg, &fakeEmbedder{}).Run(context.Background())
	var mm *ErrModelMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Config, ":512") || mm.Documents != 8 {
		t.Fatalf("want model mismatch, got %v", err)
	}
	n, err := Reembed(context.Background(), root, cfg)
	if err != nil || n != 8 {
		t.Fatalf("reembed: %d %v", n, err)
	}
	sum, err := runner(root, cfg, &fakeEmbedder{}).Run(context.Background())
	if err != nil || sum.Indexed != 6 {
		t.Fatalf("rebuild: %+v %v", sum, err)
	}
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var blob []byte
		db.QueryRowContext(ctx, `SELECT embedding FROM chunks LIMIT 1`).Scan(&blob)
		if len(blob) != 512*4 {
			t.Errorf("vectors are %d bytes, want 512 dims", len(blob))
		}
		return nil
	})
}
