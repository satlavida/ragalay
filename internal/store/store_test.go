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
