package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestDocumentLifecycle(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		var a, b int64
		err := Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			if a, err = InsertDocument(ctx, tx, Document{Path: "a.md", Kind: "markdown", SHA256: "x", Size: 1, MTime: 1}); err != nil {
				return err
			}
			if b, err = InsertDocument(ctx, tx, Document{Path: "b.pdf", Kind: "pdf", SHA256: "y", Size: 2, MTime: 2}); err != nil {
				return err
			}
			if err := SetPair(ctx, tx, a, b); err != nil {
				return err
			}
			if err := SetPair(ctx, tx, b, a); err != nil {
				return err
			}
			// Chunks and BM25 postings for both: term 1 appears in a and b, term 2 only in a.
			for _, s := range []string{
				`INSERT INTO chunks (id, document_id, ord, modality, text) VALUES (10, 1, 0, 'text', 'x'), (11, 1, 1, 'text', 'y'), (20, 2, 0, 'text', 'z')`,
				`INSERT INTO terms (id, term, df) VALUES (1, 'attention', 3), (2, 'encoder', 1)`,
				`INSERT INTO postings (term_id, chunk_id, tf) VALUES (1, 10, 1), (1, 11, 2), (1, 20, 1), (2, 10, 1)`,
				`UPDATE documents SET status = 'processing' WHERE id = 2`,
				`UPDATE jobs SET state = 'running' WHERE document_id = 2`,
			} {
				if _, err := tx.ExecContext(ctx, s); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if n, _ := QueuedJobs(ctx, db); n != 1 {
			t.Errorf("queued jobs = %d, want 1", n)
		}
		err = Tx(ctx, db, func(tx *sql.Tx) error {
			n, err := RecoverInterrupted(ctx, tx)
			if n != 1 {
				t.Errorf("recovered %d, want 1", n)
			}
			if err != nil {
				return err
			}
			if err := Move(ctx, tx, a, "notes/a.md", 1, 5); err != nil {
				return err
			}
			return DeleteDocument(ctx, tx, a)
		})
		if err != nil {
			return err
		}
		docs, err := ListDocuments(ctx, db, "", "")
		if err != nil {
			return err
		}
		if len(docs) != 1 || docs[0].Path != "b.pdf" || docs[0].Status != StatusPending || docs[0].PairID != 0 {
			t.Errorf("after delete: %+v", docs)
		}
		var df1, df2, postings, chunks int
		db.QueryRowContext(ctx, `SELECT df FROM terms WHERE id = 1`).Scan(&df1)
		db.QueryRowContext(ctx, `SELECT df FROM terms WHERE id = 2`).Scan(&df2)
		db.QueryRowContext(ctx, `SELECT count(*) FROM postings`).Scan(&postings)
		db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunks)
		if df1 != 1 || df2 != 0 || postings != 1 || chunks != 1 {
			t.Errorf("df1=%d df2=%d postings=%d chunks=%d, want 1 0 1 1", df1, df2, postings, chunks)
		}
		if n, _ := QueuedJobs(ctx, db); n != 1 {
			t.Errorf("queued jobs after recovery/delete = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
