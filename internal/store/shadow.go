package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// A model switch builds the new vector space in shadow tables while search
// keeps reading the live ones, then swaps them in one transaction (plan2
// §5.6, S1–S4). The documents and jobs tables track the space being built;
// search never reads document status.

// Tables names one set of index tables.
type Tables struct{ Chunks, Terms, Postings string }

var (
	// LiveTables are what search reads.
	LiveTables = Tables{"chunks", "terms", "postings"}
	// NextTables hold a model switch in progress.
	NextTables = Tables{"chunks_next", "terms_next", "postings_next"}
)

// Meta keys of the vector spaces. embed_config / next_embed_config hold the
// settings (config.Embed as JSON) that produce each space, so search can
// keep using the live model after the settings change.
const (
	MetaEmbedID         = "embed_id"
	MetaEmbedDim        = "embed_dim"
	MetaEmbedConfig     = "embed_config"
	MetaNextEmbedID     = "next_embed_id"
	MetaNextEmbedDim    = "next_embed_dim"
	MetaNextEmbedConfig = "next_embed_config"
)

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Target is where indexing writes: the shadow tables during a switch,
// otherwise the live ones.
type Target struct {
	Tables
	EmbedID  string
	Dim      int
	Building bool
}

// WriteTarget returns where chunks are written now.
func WriteTarget(ctx context.Context, q queryRower) (Target, error) {
	next, err := Meta(ctx, q, MetaNextEmbedID)
	if err != nil {
		return Target{}, err
	}
	if next != "" {
		d, err := Meta(ctx, q, MetaNextEmbedDim)
		if err != nil {
			return Target{}, err
		}
		dim, _ := strconv.Atoi(d)
		return Target{Tables: NextTables, EmbedID: next, Dim: dim, Building: true}, nil
	}
	live, err := Meta(ctx, q, MetaEmbedID)
	if err != nil {
		return Target{}, err
	}
	dim, err := EmbedDim(ctx, q)
	return Target{Tables: LiveTables, EmbedID: live, Dim: dim}, err
}

// Shadow is a model switch in progress.
type Shadow struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Dim     int    `json:"dim"`
	Done    int    `json:"done"`    // documents finished in the new space
	Total   int    `json:"total"`   // documents
	Changes int    `json:"changes"` // file changes that reach search when the switch finishes (S2)
}

// ReadShadow returns the switch in progress, or nil.
func ReadShadow(ctx context.Context, db *sql.DB) (*Shadow, error) {
	t, err := WriteTarget(ctx, db)
	if err != nil || !t.Building {
		return nil, err
	}
	s := &Shadow{To: t.EmbedID, Dim: t.Dim}
	if s.From, err = Meta(ctx, db, MetaEmbedID); err != nil {
		return nil, err
	}
	err = db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(CASE WHEN status = ? AND embed_id = ? THEN 1 ELSE 0 END), 0)
		FROM documents`, StatusDone, t.EmbedID).Scan(&s.Total, &s.Done)
	if err != nil {
		return nil, err
	}
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM shadow_dirty`).Scan(&s.Changes)
	return s, err
}

func tableDDL(t Tables, dim int) []string {
	return []string{
		chunksTableDDL(t.Chunks, dim),
		fmt.Sprintf(`CREATE INDEX %s_document ON %s (document_id)`, t.Chunks, t.Chunks),
		fmt.Sprintf(`CREATE TABLE %s (id INTEGER PRIMARY KEY, term TEXT NOT NULL UNIQUE, df INTEGER NOT NULL DEFAULT 0)`, t.Terms),
		fmt.Sprintf(`CREATE TABLE %s (
			term_id INTEGER NOT NULL,
			chunk_id INTEGER NOT NULL,
			tf INTEGER NOT NULL,
			PRIMARY KEY (term_id, chunk_id))`, t.Postings),
		fmt.Sprintf(`CREATE INDEX %s_chunk ON %s (chunk_id)`, t.Postings, t.Postings),
	}
}

func dropTables(ctx context.Context, tx *sql.Tx, t Tables) error {
	for _, name := range []string{t.Postings, t.Terms, t.Chunks} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+name); err != nil {
			return err
		}
	}
	return nil
}

func execAll(ctx context.Context, tx *sql.Tx, stmts []string, args ...any) error {
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s, args...); err != nil {
			return fmt.Errorf("%w\n%s", err, s)
		}
	}
	return nil
}

// StartShadow begins (or restarts, S3) a switch to embedID: empty shadow
// tables at dim, and every document queued for the new space. Search keeps
// the live tables. Returns the number of documents queued.
func StartShadow(ctx context.Context, tx *sql.Tx, embedID string, dim int, embedConfig string) (int64, error) {
	if err := dropTables(ctx, tx, NextTables); err != nil {
		return 0, err
	}
	if err := execAll(ctx, tx, tableDDL(NextTables, dim)); err != nil {
		return 0, err
	}
	for k, v := range map[string]string{MetaNextEmbedID: embedID, MetaNextEmbedDim: strconv.Itoa(dim),
		MetaNextEmbedConfig: embedConfig} {
		if err := SetMeta(ctx, tx, k, v); err != nil {
			return 0, err
		}
	}
	return queueAll(ctx, tx)
}

func queueAll(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `UPDATE documents SET status = ?, error = NULL`, StatusPending)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs (document_id, state, attempts, enqueued_at)
		SELECT id, 'queued', 0, ? FROM documents WHERE true
		ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0, last_error = NULL`, time.Now().Unix())
	return n, err
}

// CancelShadow drops a switch in progress (the user chose the live model
// again, S3). Documents that changed during the switch, and documents with
// nothing in the live index, are queued for the live space; everything
// else is already current there. Returns the number queued.
func CancelShadow(ctx context.Context, tx *sql.Tx) (int64, error) {
	if err := dropTables(ctx, tx, NextTables); err != nil {
		return 0, err
	}
	for _, k := range []string{MetaNextEmbedID, MetaNextEmbedDim, MetaNextEmbedConfig} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, k); err != nil {
			return 0, err
		}
	}
	live, err := Meta(ctx, tx, MetaEmbedID)
	if err != nil {
		return 0, err
	}
	err = execAll(ctx, tx, []string{
		`UPDATE documents SET chunk_count = (SELECT count(*) FROM chunks c WHERE c.document_id = documents.id)`,
		`INSERT OR IGNORE INTO shadow_dirty (document_id) SELECT id FROM documents WHERE chunk_count = 0`,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = ?, embed_id = ?
		WHERE id NOT IN (SELECT document_id FROM shadow_dirty)`, StatusDone, live); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE document_id NOT IN (SELECT document_id FROM shadow_dirty)`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE documents SET status = ? WHERE id IN (SELECT document_id FROM shadow_dirty)`, StatusPending)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	err = execAll(ctx, tx, []string{
		`INSERT INTO jobs (document_id, state, attempts, enqueued_at)
		SELECT document_id, 'queued', 0, ? FROM shadow_dirty WHERE true
		ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0, last_error = NULL`,
	}, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM shadow_dirty`)
	return n, err
}

// ShadowComplete reports whether nothing is left to embed for the switch:
// no queued or running jobs (failed ones are retried after the swap).
func ShadowComplete(ctx context.Context, q queryRower) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`).Scan(&n)
	return n == 0, err
}

// FinishShadow swaps the shadow tables in: they become the live index, the
// old tables are dropped and the query cache is cleared.
func FinishShadow(ctx context.Context, tx *sql.Tx) error {
	t, err := WriteTarget(ctx, tx)
	if err != nil {
		return err
	}
	if !t.Building {
		return fmt.Errorf("no model switch in progress")
	}
	cfgJSON, err := Meta(ctx, tx, MetaNextEmbedConfig)
	if err != nil {
		return err
	}
	if err := dropTables(ctx, tx, LiveTables); err != nil {
		return err
	}
	err = execAll(ctx, tx, []string{
		`DROP INDEX IF EXISTS chunks_next_document`,
		`DROP INDEX IF EXISTS postings_next_chunk`,
		`ALTER TABLE chunks_next RENAME TO chunks`,
		`ALTER TABLE terms_next RENAME TO terms`,
		`ALTER TABLE postings_next RENAME TO postings`,
		`CREATE INDEX chunks_document ON chunks (document_id)`,
		`CREATE INDEX postings_chunk ON postings (chunk_id)`,
		`DELETE FROM query_cache`,
		`DELETE FROM shadow_dirty`,
		`DELETE FROM meta WHERE key IN ('next_embed_id', 'next_embed_dim', 'next_embed_config')`,
	})
	if err != nil {
		return err
	}
	return SetLiveSpace(ctx, tx, t.EmbedID, t.Dim, cfgJSON)
}

// SetLiveSpace records the live index's vector space and the settings that
// produce it.
func SetLiveSpace(ctx context.Context, tx *sql.Tx, embedID string, dim int, embedConfig string) error {
	if err := SetMeta(ctx, tx, MetaEmbedID, embedID); err != nil {
		return err
	}
	if err := SetMeta(ctx, tx, MetaEmbedDim, strconv.Itoa(dim)); err != nil {
		return err
	}
	if embedConfig == "" {
		_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, MetaEmbedConfig)
		return err
	}
	return SetMeta(ctx, tx, MetaEmbedConfig, embedConfig)
}
