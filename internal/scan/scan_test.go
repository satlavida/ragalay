package scan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
)

// newRoot creates an initialised ragalay folder.
func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, config.DirName), 0o755)
	if err := config.Save(root, config.Default()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err := store.With(ctx, filepath.Join(root, config.DirName, config.DBFile), func(db *sql.DB) error {
		return store.Migrate(ctx, db, 1024)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func docs(t *testing.T, root string) map[string]store.Document {
	t.Helper()
	ctx := context.Background()
	out := map[string]store.Document{}
	err := store.With(ctx, filepath.Join(root, config.DirName, config.DBFile), func(db *sql.DB) error {
		ds, err := store.ListDocuments(ctx, db, "", "")
		for _, d := range ds {
			out[d.Path] = d
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustScan(t *testing.T, root string, cfg config.Config) Report {
	t.Helper()
	rep, err := Run(context.Background(), root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestMatcher(t *testing.T) {
	m, err := NewMatcher([]string{"**/node_modules/**", "drafts/*.md", "*.tmp.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rel  string
		dir  bool
		skip bool
	}{
		{"node_modules", true, true},
		{"a/b/node_modules", true, true},
		{"a/node_modules/x.md", false, true},
		{"drafts/x.md", false, true},
		{"drafts/sub/x.md", false, false},
		{"x.tmp.pdf", false, true},
		{"docs/x.tmp.pdf", false, false},
		{".git", true, true},
		{"docs/.hidden.md", false, true},
		{"docs/notes.md", false, false},
	} {
		if got := m.Skip(c.rel, c.dir); got != c.skip {
			t.Errorf("Skip(%q, dir=%v) = %v, want %v", c.rel, c.dir, got, c.skip)
		}
	}
}

func TestKindOf(t *testing.T) {
	all := []string{"md", "pdf", "image"}
	for p, want := range map[string]string{"a.MD": KindMarkdown, "b.markdown": KindMarkdown, "c.PDF": KindPDF, "d.JPeg": KindImage, "e.webp": KindImage} {
		if got, ok := KindOf(p, all); !ok || got != want {
			t.Errorf("KindOf(%s) = %s %v", p, got, ok)
		}
	}
	if _, ok := KindOf("x.txt", all); ok {
		t.Error(".txt is not a document")
	}
	if _, ok := KindOf("x.png", []string{"md"}); ok {
		t.Error("disabled kinds must be skipped")
	}
}

// TestScanLifecycle is plan1 Phase 3's exit criterion: add, edit, move,
// delete and pair files and check the index follows.
func TestScanLifecycle(t *testing.T) {
	root := newRoot(t)
	cfg := config.Default()
	write(t, root, "docs/a.md", "# A\nfirst")
	write(t, root, "docs/b.pdf", "%PDF-fake-b")
	write(t, root, "photos/c.png", "png-c")
	write(t, root, "docs/notes.txt", "not indexed")
	write(t, root, "node_modules/pkg/readme.md", "ignored")
	write(t, root, ".obsidian/x.md", "hidden")

	rep := mustScan(t, root, cfg)
	slices.Sort(rep.New)
	if !slices.Equal(rep.New, []string{"docs/a.md", "docs/b.pdf", "photos/c.png"}) || rep.Queued != 3 {
		t.Fatalf("first scan: %+v", rep)
	}
	first := docs(t, root)
	if first["docs/a.md"].Kind != KindMarkdown || first["docs/a.md"].Status != store.StatusPending ||
		len(first["docs/a.md"].SHA256) != 64 {
		t.Fatalf("bad document row: %+v", first["docs/a.md"])
	}

	// Nothing changed: nothing to do.
	if rep := mustScan(t, root, cfg); rep.Changes() || rep.Unchanged != 3 {
		t.Fatalf("rescan without changes: %+v", rep)
	}

	// Edit a.md; only touch c.png (same bytes, new mtime).
	write(t, root, "docs/a.md", "# A\nsecond version")
	later := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(root, "photos", "c.png"), later, later)
	rep = mustScan(t, root, cfg)
	if !slices.Equal(rep.Changed, []string{"docs/a.md"}) || len(rep.New)+len(rep.Deleted) != 0 || rep.Unchanged != 2 {
		t.Fatalf("edit scan: %+v", rep)
	}

	// Move b.pdf: same id, new path, embeddings kept.
	os.MkdirAll(filepath.Join(root, "archive"), 0o755)
	os.Rename(filepath.Join(root, "docs", "b.pdf"), filepath.Join(root, "archive", "b-renamed.pdf"))
	rep = mustScan(t, root, cfg)
	if len(rep.Moved) != 1 || rep.Moved[0] != (Move{"docs/b.pdf", "archive/b-renamed.pdf"}) || len(rep.Deleted) != 0 || len(rep.New) != 0 {
		t.Fatalf("move scan: %+v", rep)
	}
	if d := docs(t, root)["archive/b-renamed.pdf"]; d.ID != first["docs/b.pdf"].ID {
		t.Fatalf("moved document got a new id: %+v", d)
	}

	// Delete c.png.
	os.Remove(filepath.Join(root, "photos", "c.png"))
	rep = mustScan(t, root, cfg)
	if !slices.Equal(rep.Deleted, []string{"photos/c.png"}) {
		t.Fatalf("delete scan: %+v", rep)
	}
	if _, ok := docs(t, root)["photos/c.png"]; ok {
		t.Fatal("deleted file still indexed")
	}
}

func TestFoldersAndStale(t *testing.T) {
	root := newRoot(t)
	write(t, root, "docs/a.md", "a")
	write(t, root, "notes/b.md", "b")
	cfg := config.Default()
	cfg.Scan.Folders = []string{"docs"}
	if rep := mustScan(t, root, cfg); !slices.Equal(rep.New, []string{"docs/a.md"}) {
		t.Fatalf("folder-limited scan: %+v", rep)
	}

	// Switch to notes, keeping docs (folders remove --keep): a.md stays, but
	// new files in docs are not picked up.
	cfg.Scan.Folders = []string{"notes"}
	cfg.Scan.Keep = []string{"docs"}
	write(t, root, "docs/new.md", "added while kept")
	rep := mustScan(t, root, cfg)
	if !slices.Equal(rep.New, []string{"notes/b.md"}) || len(rep.Deleted) != 0 {
		t.Fatalf("kept documents must survive and kept folders must not be scanned: %+v", rep)
	}

	// Stop keeping docs (or edit config.toml by hand): a.md goes.
	cfg.Scan.Keep = nil
	if rep := mustScan(t, root, cfg); !slices.Equal(rep.Deleted, []string{"docs/a.md"}) {
		t.Fatalf("documents outside folders and keep must be dropped: %+v", rep)
	}
	ctx := context.Background()

	// Stale but back in scope before the next scan: revived, not re-added.
	store.With(ctx, filepath.Join(root, config.DirName, config.DBFile), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error { _, err := store.MarkStaleUnder(ctx, tx, "notes"); return err })
	})
	rep = mustScan(t, root, cfg)
	if !slices.Equal(rep.Revived, []string{"notes/b.md"}) || len(rep.New)+len(rep.Deleted) != 0 {
		t.Fatalf("revive: %+v", rep)
	}
	if d := docs(t, root)["notes/b.md"]; d.Status != store.StatusPending {
		t.Fatalf("revived never-indexed document should be pending: %+v", d)
	}
	if rep.Missing == nil || len(rep.Missing) != 0 {
		t.Fatalf("missing folders: %v", rep.Missing)
	}
	cfg.Scan.Folders = []string{"gone"}
	if rep := mustScan(t, root, cfg); !slices.Equal(rep.Missing, []string{"gone"}) {
		t.Fatalf("missing folder not reported: %+v", rep.Missing)
	}
}

func TestPairing(t *testing.T) {
	root := newRoot(t)
	cfg := config.Default()
	cfg.Pairs = []config.Pair{{MD: "md", PDF: "pdf"}}
	// Mirrored folders.
	write(t, root, "md/2024/report.md", "# Report")
	write(t, root, "pdf/2024/report.pdf", "%PDF-r")
	// Front matter, pointing somewhere unusual.
	write(t, root, "notes/summary.md", "---\ntitle: x\nsource: ../scans/scan-0042.pdf\n---\n# Summary")
	write(t, root, "scans/scan-0042.pdf", "%PDF-s")
	// Same base name in different folders.
	write(t, root, "transcripts/budget.md", "# Budget")
	write(t, root, "finance/budget.pdf", "%PDF-b")
	// Ambiguous: two invoice.pdf files.
	write(t, root, "transcripts/invoice.md", "# Invoice")
	write(t, root, "a/invoice.pdf", "%PDF-i1")
	write(t, root, "b/invoice.pdf", "%PDF-i2")
	// Broken front matter source.
	write(t, root, "notes/broken.md", "---\nsource: nope.pdf\n---\n")

	rep := mustScan(t, root, cfg)
	got := map[string]string{}
	for _, p := range rep.Pairs {
		got[p.MD] = p.PDF + " via " + p.Via
	}
	want := map[string]string{
		"md/2024/report.md":     "pdf/2024/report.pdf via pairs config",
		"notes/summary.md":      "scans/scan-0042.pdf via front matter",
		"transcripts/budget.md": "finance/budget.pdf via same name",
	}
	for md, w := range want {
		if got[md] != w {
			t.Errorf("pair for %s = %q, want %q", md, got[md], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected pairs: %v", got)
	}
	warn := strings.Join(rep.Warnings, "\n")
	if !strings.Contains(warn, "several PDFs named invoice.pdf") || !strings.Contains(warn, "nope.pdf is not an indexed PDF") {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	d := docs(t, root)
	if d["notes/summary.md"].PairID != d["scans/scan-0042.pdf"].ID || d["scans/scan-0042.pdf"].PairID != d["notes/summary.md"].ID {
		t.Error("pairs must be stored both ways")
	}

	// Removing the PDF unpairs the transcription.
	os.Remove(filepath.Join(root, "finance", "budget.pdf"))
	mustScan(t, root, cfg)
	if d := docs(t, root)["transcripts/budget.md"]; d.PairID != 0 {
		t.Errorf("pair not cleared after the PDF was deleted: %+v", d)
	}
}

func TestPDFRequeuedWhenItsTranscriptionGoes(t *testing.T) {
	root := newRoot(t)
	write(t, root, "md/report.md", "# Report")
	write(t, root, "pdf/report.pdf", "%PDF")
	mustScan(t, root, config.Default())
	ctx := context.Background()
	db := filepath.Join(root, config.DirName, config.DBFile)
	// Pretend both were indexed.
	store.With(ctx, db, func(d *sql.DB) error {
		_, err := d.ExecContext(ctx, `UPDATE documents SET status = 'done'`)
		d.ExecContext(ctx, `DELETE FROM jobs`)
		return err
	})
	os.Remove(filepath.Join(root, "md", "report.md"))
	rep := mustScan(t, root, config.Default())
	if got := docs(t, root)["pdf/report.pdf"]; got.Status != store.StatusPending || got.PairID != 0 || rep.Queued != 1 {
		t.Fatalf("PDF must be re-indexed with its own text: %+v queued=%d", got, rep.Queued)
	}
}

func TestPairPrecedenceIgnoresFileOrder(t *testing.T) {
	// a.md (sorted first) would take report.pdf by name, but z.md claims it
	// through front matter, which must win.
	root := newRoot(t)
	write(t, root, "a/report.md", "# by name")
	write(t, root, "z/zz.md", "---\nsource: ../pdf/report.pdf\n---\n")
	write(t, root, "pdf/report.pdf", "%PDF")
	rep := mustScan(t, root, config.Default())
	if len(rep.Pairs) != 1 || rep.Pairs[0].MD != "z/zz.md" {
		t.Fatalf("front matter must win: %+v", rep.Pairs)
	}
}

func TestWatchRescansOnChanges(t *testing.T) {
	root := newRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reports := make(chan Report, 20)
	go Watch(ctx, root, 200*time.Millisecond, func(r Report, err error) {
		if err == nil {
			reports <- r
		}
	})
	next := func(what string, ok func(Report) bool) {
		t.Helper()
		for {
			select {
			case r := <-reports:
				if ok(r) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	next("initial scan", func(r Report) bool { return true })

	write(t, root, "new/folder/doc.md", "# created after the watch started")
	next("new file in a new folder", func(r Report) bool { return slices.Contains(r.New, "new/folder/doc.md") })

	os.Remove(filepath.Join(root, "new", "folder", "doc.md"))
	next("deletion", func(r Report) bool { return slices.Contains(r.Deleted, "new/folder/doc.md") })

	// Narrow the folders in config.toml: the watcher reloads it.
	write(t, root, "keep/a.md", "a")
	write(t, root, "drop/b.md", "b")
	next("files before config change", func(r Report) bool { return len(r.New) > 0 })
	cfg := config.Default()
	cfg.Scan.Folders = []string{"keep"}
	config.Save(root, cfg)
	next("config reload", func(r Report) bool { return slices.Contains(r.Deleted, "drop/b.md") })
}
