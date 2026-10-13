package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.db")
	ctx := context.Background()
	if err := With(ctx, path, func(db *sql.DB) error { return Migrate(ctx, db, 1024) }); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return path
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		if err := Migrate(ctx, db, 1024); err != nil {
			return err
		}
		v, err := Version(ctx, db)
		if err != nil {
			return err
		}
		if v != SchemaVersion {
			t.Errorf("version = %d, want %d", v, SchemaVersion)
		}
		dim, err := Meta(ctx, db, "embed_dim")
		if dim != "1024" {
			t.Errorf("embed_dim = %q, want 1024", dim)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		if err := Tx(ctx, db, func(tx *sql.Tx) error { return SetMeta(ctx, tx, "schema_version", "99") }); err != nil {
			return err
		}
		return Migrate(ctx, db, 1024)
	})
	if err == nil {
		t.Fatal("expected an error for a newer schema")
	}
}

func TestStatsAndFolderQueries(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		err := Tx(ctx, db, func(tx *sql.Tx) error {
			for _, d := range []struct{ path, kind, status string }{
				{"docs/a.md", "markdown", "done"},
				{"docs/sub/b.pdf", "pdf", "pending"},
				{"docsX/c.md", "markdown", "done"},
				{"photos/d.jpg", "image", "failed"},
			} {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO documents (path, kind, status, added_at) VALUES (?, ?, ?, ?)`,
					d.path, d.kind, d.status, time.Now().Unix()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		s, err := ReadStats(ctx, db)
		if err != nil {
			return err
		}
		if s.Documents != 4 || s.ByStatus["done"] != 2 || s.ByKind["markdown"] != 2 || s.SetupComplete {
			t.Errorf("unexpected stats: %+v", s)
		}
		n, err := CountUnder(ctx, db, "docs")
		if err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("CountUnder(docs) = %d, want 2 (docsX must not match)", n)
		}
		return Tx(ctx, db, func(tx *sql.Tx) error {
			got, err := MarkStaleUnder(ctx, tx, "docs")
			if got != 2 {
				t.Errorf("MarkStaleUnder(docs) = %d, want 2", got)
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIsLocked(t *testing.T) {
	if !IsLocked(errors.New("turso: error: Locking error: Failed locking file. File is locked by another process")) {
		t.Error("Turso lock error not recognised")
	}
	if IsLocked(errors.New("no such table: x")) || IsLocked(nil) {
		t.Error("false positive")
	}
}

func TestMigrateV3ToV4AddsTimeColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	err := With(ctx, path, func(db *sql.DB) error {
		// Build a schema-3 database by running the first three migrations.
		for v := 0; v < 3; v++ {
			if err := Tx(ctx, db, func(tx *sql.Tx) error {
				if err := migrations[v](ctx, tx, 8); err != nil {
					return err
				}
				return SetMeta(ctx, tx, "schema_version", string(rune('1'+v)))
			}); err != nil {
				return err
			}
		}
		if err := Migrate(ctx, db, 8); err != nil {
			return err
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('chunks') WHERE name IN ('start_ms', 'end_ms')`).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("start_ms/end_ms columns = %d, want 2", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaceChunksStoresTimeRange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	err := With(ctx, path, func(db *sql.DB) error {
		if err := Migrate(ctx, db, 2); err != nil {
			return err
		}
		return Tx(ctx, db, func(tx *sql.Tx) error {
			id, err := InsertDocument(ctx, tx, Document{Path: "a.wav", Kind: "audio", SHA256: "x", Size: 1, MTime: 1})
			if err != nil {
				return err
			}
			rows := []ChunkRow{
				{Modality: "audio", HeadingPath: "0:00-0:30", StartMS: 0, EndMS: 30000, Vector: []float32{1, 0}},
				{Modality: "text", Text: "plain", Vector: []float32{0, 1}},
			}
			if err := ReplaceChunks(ctx, tx, id, rows); err != nil {
				return err
			}
			var s, e sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT start_ms, end_ms FROM chunks WHERE ord = 0`).Scan(&s, &e); err != nil {
				return err
			}
			if !s.Valid || s.Int64 != 0 || e.Int64 != 30000 {
				t.Errorf("audio chunk range = %v-%v", s, e)
			}
			if err := tx.QueryRowContext(ctx, `SELECT start_ms FROM chunks WHERE ord = 1`).Scan(&s); err != nil {
				return err
			}
			if s.Valid {
				t.Error("a text chunk should have no time range")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
