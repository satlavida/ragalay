package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// SchemaVersion is the schema this binary writes. Migrations upgrade older
// databases step by step; a newer database is refused.
const SchemaVersion = 1

// migrations[i] upgrades from version i to i+1. The chunks table depends on
// the embedding dimension, so steps get it as a parameter.
var migrations = []func(ctx context.Context, tx *sql.Tx, dim int) error{
	migrateV1,
}

func migrateV1(ctx context.Context, tx *sql.Tx, dim int) error {
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE documents (
			id INTEGER PRIMARY KEY,
			path TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			sha256 TEXT,
			size INTEGER,
			mtime INTEGER,
			status TEXT NOT NULL DEFAULT 'pending',
			error TEXT,
			chunk_count INTEGER NOT NULL DEFAULT 0,
			pair_document_id INTEGER,
			embed_id TEXT,
			added_at INTEGER NOT NULL,
			processed_at INTEGER)`,
		`CREATE INDEX documents_sha256 ON documents (sha256)`,
		`CREATE INDEX documents_status ON documents (status)`,
		`CREATE TABLE doc_links (
			parent_id INTEGER NOT NULL,
			child_id INTEGER NOT NULL,
			heading_path TEXT,
			alt_text TEXT,
			PRIMARY KEY (parent_id, child_id))`,
		chunksDDL(dim),
		`CREATE INDEX chunks_document ON chunks (document_id)`,
		`CREATE TABLE terms (id INTEGER PRIMARY KEY, term TEXT NOT NULL UNIQUE, df INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE postings (
			term_id INTEGER NOT NULL,
			chunk_id INTEGER NOT NULL,
			tf INTEGER NOT NULL,
			PRIMARY KEY (term_id, chunk_id))`,
		`CREATE INDEX postings_chunk ON postings (chunk_id)`,
		`CREATE TABLE query_cache (
			embed_id TEXT NOT NULL,
			query_norm TEXT NOT NULL,
			vector BLOB NOT NULL,
			hit_count INTEGER NOT NULL DEFAULT 1,
			last_used INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (embed_id, query_norm))`,
		`CREATE TABLE jobs (
			document_id INTEGER PRIMARY KEY,
			state TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			enqueued_at INTEGER NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%w\n%s", err, s)
		}
	}
	if err := SetMeta(ctx, tx, "embed_dim", strconv.Itoa(dim)); err != nil {
		return err
	}
	return SetMeta(ctx, tx, "created_at", strconv.FormatInt(time.Now().Unix(), 10))
}

// chunksDDL is shared with re-embedding, which recreates the table when the
// dimension changes. Turso does not enforce the declared F32_BLOB dimension,
// so writers must check vector length before inserting.
func chunksDDL(dim int) string {
	return fmt.Sprintf(`CREATE TABLE chunks (
		id INTEGER PRIMARY KEY,
		document_id INTEGER NOT NULL,
		ord INTEGER NOT NULL,
		modality TEXT NOT NULL,
		text TEXT,
		heading_path TEXT,
		page INTEGER,
		token_count INTEGER NOT NULL DEFAULT 0,
		embedding F32_BLOB(%d))`, dim)
}

// Version returns the database's schema version, 0 for an empty database.
func Version(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'meta'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	v, err := Meta(ctx, db, "schema_version")
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.Atoi(v)
}

// Migrate brings db up to SchemaVersion. dim is only used when creating the
// chunks table for the first time.
func Migrate(ctx context.Context, db *sql.DB, dim int) error {
	v, err := Version(ctx, db)
	if err != nil {
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("index.db has schema version %d, but this ragalay only knows %d; update ragalay", v, SchemaVersion)
	}
	for ; v < SchemaVersion; v++ {
		next := v + 1
		err := Tx(ctx, db, func(tx *sql.Tx) error {
			if err := migrations[v](ctx, tx, dim); err != nil {
				return fmt.Errorf("migrate to schema %d: %w", next, err)
			}
			return SetMeta(ctx, tx, "schema_version", strconv.Itoa(next))
		})
		if err != nil {
			return err
		}
	}
	return nil
}
