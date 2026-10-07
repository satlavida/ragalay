package store

import (
	"context"
	"database/sql"
	"testing"
)

func count(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func mustTx(t *testing.T, ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := Tx(ctx, db, fn); err != nil {
		t.Fatal(err)
	}
}

// liveDB has two indexed documents in space "old" (dim 1024).
func liveDB(t *testing.T, ctx context.Context, db *sql.DB) (a, b int64) {
	a = insertDoc(t, ctx, db, "a.md", "markdown")
	b = insertDoc(t, ctx, db, "b.md", "markdown")
	mustTx(t, ctx, db, func(tx *sql.Tx) error {
		if err := SetLiveSpace(ctx, tx, "old", 1024, `{"profile":"old"}`); err != nil {
			return err
		}
		for i, id := range []int64{a, b} {
			if err := ReplaceChunks(ctx, tx, id, []ChunkRow{{Modality: "text", Text: "attention heads", Vector: vec(1024, float32(i+1))}}); err != nil {
				return err
			}
			if err := MarkDone(ctx, tx, id, 1, "old", ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err := PutQuery(ctx, db, "old", "q", vec(1024, 1)); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestShadowBuildKeepsLiveIndexAndSwaps(t *testing.T) {
	ctx := context.Background()
	err := With(ctx, newDB(t), func(db *sql.DB) error {
		a, b := liveDB(t, ctx, db)
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			n, err := StartShadow(ctx, tx, "new", 256, `{"profile":"new"}`)
			if n != 2 {
				t.Errorf("queued %d", n)
			}
			return err
		})
		// Search's tables are untouched while the switch runs.
		if c := count(t, ctx, db, `SELECT count(*) FROM chunks`); c != 2 {
			t.Fatalf("live chunks during switch: %d", c)
		}
		tgt, _ := WriteTarget(ctx, db)
		if !tgt.Building || tgt.Dim != 256 || tgt.Chunks != "chunks_next" {
			t.Fatalf("target %+v", tgt)
		}
		// The new space is written to the shadow, at its dimension.
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			if err := ReplaceChunks(ctx, tx, a, []ChunkRow{{Modality: "text", Text: "multi head attention", Vector: vec(256, 1)}}); err != nil {
				return err
			}
			return MarkDone(ctx, tx, a, 1, "new", "")
		})
		if c := count(t, ctx, db, `SELECT count(*) FROM chunks_next`); c != 1 {
			t.Fatalf("shadow chunks %d", c)
		}
		// A file change during the switch is remembered (S2); a deleted file
		// leaves both indexes.
		mustTx(t, ctx, db, func(tx *sql.Tx) error { return Enqueue(ctx, tx, b) })
		c := insertDoc(t, ctx, db, "c.md", "markdown")
		mustTx(t, ctx, db, func(tx *sql.Tx) error { return DeleteDocument(ctx, tx, c) })
		s, err := ReadShadow(ctx, db)
		if err != nil || s == nil || s.From != "old" || s.To != "new" || s.Done != 1 || s.Total != 2 || s.Changes != 1 {
			t.Fatalf("shadow %+v %v", s, err)
		}
		if done, _ := ShadowComplete(ctx, db); done {
			t.Fatal("b is still queued")
		}
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			if err := ReplaceChunks(ctx, tx, b, nil); err != nil {
				return err
			}
			return MarkDone(ctx, tx, b, 0, "new", "")
		})
		if done, _ := ShadowComplete(ctx, db); !done {
			t.Fatal("switch should be complete")
		}
		mustTx(t, ctx, db, func(tx *sql.Tx) error { return FinishShadow(ctx, tx) })

		id, _ := Meta(ctx, db, MetaEmbedID)
		cfg, _ := Meta(ctx, db, MetaEmbedConfig)
		dim, _ := EmbedDim(ctx, db)
		if id != "new" || dim != 256 || cfg != `{"profile":"new"}` {
			t.Fatalf("after swap: %s %d %s", id, dim, cfg)
		}
		if n := count(t, ctx, db, `SELECT count(*) FROM chunks`); n != 1 {
			t.Fatalf("live chunks after swap %d", n)
		}
		if n := count(t, ctx, db, `SELECT count(*) FROM query_cache`) + count(t, ctx, db, `SELECT count(*) FROM shadow_dirty`); n != 0 {
			t.Fatal("cache and dirty list must be cleared")
		}
		if n := count(t, ctx, db, `SELECT count(*) FROM sqlite_schema WHERE name LIKE '%_next%'`); n != 0 {
			t.Fatalf("%d shadow objects left", n)
		}
		if df(t, ctx, db, "multi") != 1 || df(t, ctx, db, "heads") != 0 {
			t.Fatal("BM25 terms must come from the new space")
		}
		// The swapped-in tables work like the originals.
		return Tx(ctx, db, func(tx *sql.Tx) error {
			return ReplaceChunks(ctx, tx, b, []ChunkRow{{Modality: "text", Text: "x", Vector: vec(256, 2)}})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCancelShadowRequeuesOnlyChanges(t *testing.T) {
	ctx := context.Background()
	err := With(ctx, newDB(t), func(db *sql.DB) error {
		a, b := liveDB(t, ctx, db)
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			_, err := StartShadow(ctx, tx, "new", 256, "")
			return err
		})
		mustTx(t, ctx, db, func(tx *sql.Tx) error { return Enqueue(ctx, tx, b) }) // b changed
		// Restarting (S3) keeps the change list: the live index is still stale.
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			_, err := StartShadow(ctx, tx, "newer", 128, "")
			return err
		})
		var n int64
		mustTx(t, ctx, db, func(tx *sql.Tx) error {
			var err error
			n, err = CancelShadow(ctx, tx)
			return err
		})
		if n != 1 {
			t.Fatalf("requeued %d, want only b", n)
		}
		var sa, sb string
		db.QueryRowContext(ctx, `SELECT status FROM documents WHERE id = ?`, a).Scan(&sa)
		db.QueryRowContext(ctx, `SELECT status FROM documents WHERE id = ?`, b).Scan(&sb)
		q, _ := QueuedJobs(ctx, db)
		tgt, _ := WriteTarget(ctx, db)
		if sa != StatusDone || sb != StatusPending || q != 1 || tgt.Building || tgt.EmbedID != "old" {
			t.Fatalf("after cancel: a=%s b=%s queued=%d target=%+v", sa, sb, q, tgt)
		}
		if c := count(t, ctx, db, `SELECT count(*) FROM chunks`); c != 2 {
			t.Fatalf("live chunks %d", c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
