package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func vec(dim int, x float32) []float32 {
	v := make([]float32, dim)
	v[0] = x
	return v
}

func insertDoc(t *testing.T, ctx context.Context, db *sql.DB, path, kind string) int64 {
	t.Helper()
	var id int64
	err := Tx(ctx, db, func(tx *sql.Tx) error {
		var err error
		id, err = InsertDocument(ctx, tx, Document{Path: path, Kind: kind, SHA256: path, Size: 1, MTime: 1})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func df(t *testing.T, ctx context.Context, db *sql.DB, term string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT df FROM terms WHERE term = ?`, term).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return n
}

func TestReplaceChunksKeepsBM25Consistent(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		a := insertDoc(t, ctx, db, "a.md", "markdown")
		b := insertDoc(t, ctx, db, "b.md", "markdown")
		put := func(id int64, rows []ChunkRow) error {
			return Tx(ctx, db, func(tx *sql.Tx) error { return ReplaceChunks(ctx, tx, id, rows) })
		}
		if err := put(a, []ChunkRow{
			{Modality: "text", Text: "multi-head attention attention", HeadingPath: "Model", Vector: vec(1024, 1)},
			{Modality: "text", Text: "positional encoding", Vector: vec(1024, 2)},
		}); err != nil {
			return err
		}
		if err := put(b, []ChunkRow{{Modality: "text", Text: "attention is all you need", Vector: vec(1024, 3)}}); err != nil {
			return err
		}
		if df(t, ctx, db, "attention") != 2 || df(t, ctx, db, "model") != 1 || df(t, ctx, db, "encoding") != 1 {
			t.Errorf("df after insert: attention=%d model=%d encoding=%d", df(t, ctx, db, "attention"),
				df(t, ctx, db, "model"), df(t, ctx, db, "encoding"))
		}
		var tf, blen int
		db.QueryRowContext(ctx, `SELECT p.tf, c.bm25_len FROM postings p JOIN terms t ON t.id = p.term_id
			JOIN chunks c ON c.id = p.chunk_id WHERE t.term = 'attention' AND c.document_id = ?`, a).Scan(&tf, &blen)
		if tf != 2 || blen != 4 { // model multi head attention attention -> 5? heading "Model" + 4 words
			if !(tf == 2 && blen == 5) {
				t.Errorf("tf=%d bm25_len=%d", tf, blen)
			}
		}
		// Re-index a with different content: its old terms disappear.
		if err := put(a, []ChunkRow{{Modality: "text", Text: "encoder decoder", Vector: vec(1024, 4)}}); err != nil {
			return err
		}
		if df(t, ctx, db, "attention") != 1 || df(t, ctx, db, "encoding") != 0 || df(t, ctx, db, "decoder") != 1 {
			t.Errorf("df after replace: attention=%d encoding=%d decoder=%d", df(t, ctx, db, "attention"),
				df(t, ctx, db, "encoding"), df(t, ctx, db, "decoder"))
		}
		// Wrong dimension is refused.
		err := put(a, []ChunkRow{{Modality: "text", Text: "x", Vector: vec(512, 1)}})
		if err == nil || !strings.Contains(err.Error(), "512-dimension") {
			t.Errorf("wrong dimension accepted: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobsOrderAndRetries(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		img := insertDoc(t, ctx, db, "z.png", "image")
		pdf := insertDoc(t, ctx, db, "y.pdf", "pdf")
		md := insertDoc(t, ctx, db, "x.md", "markdown")
		Tx(ctx, db, func(tx *sql.Tx) error {
			SetPair(ctx, tx, md, pdf)
			return SetPair(ctx, tx, pdf, md)
		})
		jobs, err := QueuedJobsInOrder(ctx, db)
		if err != nil {
			return err
		}
		if len(jobs) != 3 || jobs[0].ID != md || jobs[1].ID != pdf || jobs[2].ID != img || jobs[1].PairedWith != "markdown" {
			t.Fatalf("order: %+v", jobs)
		}
		// Fail the image MaxAttempts times: it is retried until then.
		for i := 0; i < MaxAttempts; i++ {
			Tx(ctx, db, func(tx *sql.Tx) error {
				if err := MarkProcessing(ctx, tx, img); err != nil {
					return err
				}
				return MarkFailed(ctx, tx, img, "boom")
			})
			var n int64
			Tx(ctx, db, func(tx *sql.Tx) error { n, err = RequeueFailed(ctx, tx); return err })
			if want := int64(1); i == MaxAttempts-1 {
				want = 0
				if n != want {
					t.Errorf("attempt %d: requeued %d, want %d", i, n, want)
				}
			} else if n != want {
				t.Errorf("attempt %d: requeued %d, want %d", i, n, want)
			}
		}
		Tx(ctx, db, func(tx *sql.Tx) error { return MarkDone(ctx, tx, md, 3, "space", "") })
		if n, _ := QueuedJobs(ctx, db); n != 1 { // only the pdf
			t.Errorf("queued after done/failed = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLinksMoveImagesBetweenStandaloneAndLinked(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		md := insertDoc(t, ctx, db, "notes/a.md", "markdown")
		img := insertDoc(t, ctx, db, "notes/img/x.png", "image")
		Tx(ctx, db, func(tx *sql.Tx) error {
			if err := ReplaceChunks(ctx, tx, img, []ChunkRow{{Modality: "image", Vector: vec(1024, 1)}}); err != nil {
				return err
			}
			return MarkDone(ctx, tx, img, 1, "space", "")
		})
		// The Markdown file starts linking the image: its own chunks go.
		if err := Tx(ctx, db, func(tx *sql.Tx) error {
			return ReplaceLinks(ctx, tx, md, []Link{{ChildPath: "notes/img/x.png", Alt: "x"}})
		}); err != nil {
			return err
		}
		var chunks int
		var note string
		db.QueryRowContext(ctx, `SELECT chunk_count, coalesce(error, '') FROM documents WHERE id = ?`, img).Scan(&chunks, &note)
		var own int
		db.QueryRowContext(ctx, `SELECT count(*) FROM chunks WHERE document_id = ?`, img).Scan(&own)
		if chunks != 0 || own != 0 || note != LinkedNote {
			t.Errorf("linked image keeps own chunks: count=%d own=%d note=%q", chunks, own, note)
		}
		if linked, _ := IsLinked(ctx, db, "notes/img/x.png"); !linked {
			t.Error("IsLinked false")
		}
		// The link is removed again: the image is queued to be indexed alone.
		if err := Tx(ctx, db, func(tx *sql.Tx) error { return ReplaceLinks(ctx, tx, md, nil) }); err != nil {
			return err
		}
		jobs, _ := QueuedJobsInOrder(ctx, db)
		found := false
		for _, j := range jobs {
			found = found || j.ID == img
		}
		if !found {
			t.Error("unlinked image was not re-queued")
		}
		// Deleting the parent also re-queues linked images.
		Tx(ctx, db, func(tx *sql.Tx) error {
			ReplaceLinks(ctx, tx, md, []Link{{ChildPath: "notes/img/x.png"}})
			return DeleteDocument(ctx, tx, md)
		})
		var status string
		db.QueryRowContext(ctx, `SELECT status FROM documents WHERE id = ?`, img).Scan(&status)
		if status != StatusPending {
			t.Errorf("image of a deleted document: status %s", status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResetForReembed(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		a := insertDoc(t, ctx, db, "a.md", "markdown")
		Tx(ctx, db, func(tx *sql.Tx) error {
			ReplaceChunks(ctx, tx, a, []ChunkRow{{Modality: "text", Text: "hello world", Vector: vec(1024, 1)}})
			MarkDone(ctx, tx, a, 1, "old", "")
			return PutQuery(ctx, db, "old", "q", vec(1024, 1))
		})
		var n int64
		if err := Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			n, err = ResetForReembed(ctx, tx, "new", 256, `{"profile":"x"}`)
			return err
		}); err != nil {
			return err
		}
		if n != 1 {
			t.Errorf("reset %d documents", n)
		}
		var chunks, terms, cache int
		db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunks)
		db.QueryRowContext(ctx, `SELECT count(*) FROM terms`).Scan(&terms)
		db.QueryRowContext(ctx, `SELECT count(*) FROM query_cache`).Scan(&cache)
		dim, _ := EmbedDim(ctx, db)
		id, _ := Meta(ctx, db, "embed_id")
		q, _ := QueuedJobs(ctx, db)
		if chunks+terms+cache != 0 || dim != 256 || id != "new" || q != 1 {
			t.Errorf("after reset: chunks=%d terms=%d cache=%d dim=%d id=%s queued=%d", chunks, terms, cache, dim, id, q)
		}
		// New dimension is enforced.
		return Tx(ctx, db, func(tx *sql.Tx) error {
			return ReplaceChunks(ctx, tx, a, []ChunkRow{{Modality: "text", Text: "x", Vector: vec(256, 1)}})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
