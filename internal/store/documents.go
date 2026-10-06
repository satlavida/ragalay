package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Document statuses.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusStale      = "stale"
)

// Document is one row of the documents table.
type Document struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	MTime       int64  `json:"mtime"` // unix nanoseconds
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	ChunkCount  int    `json:"chunk_count"`
	PairID      int64  `json:"pair_document_id,omitempty"`
	EmbedID     string `json:"embed_id,omitempty"`
	AddedAt     int64  `json:"added_at"`
	ProcessedAt int64  `json:"processed_at,omitempty"`
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const docColumns = `id, path, kind, coalesce(sha256, ''), coalesce(size, 0), coalesce(mtime, 0), status,
	coalesce(error, ''), chunk_count, coalesce(pair_document_id, 0), coalesce(embed_id, ''), added_at,
	coalesce(processed_at, 0)`

// ListDocuments returns documents, optionally filtered by status and kind
// ("" = any), ordered by path.
func ListDocuments(ctx context.Context, q querier, status, kind string) ([]Document, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+docColumns+` FROM documents
		WHERE (? = '' OR status = ?) AND (? = '' OR kind = ?) ORDER BY path`, status, status, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.Path, &d.Kind, &d.SHA256, &d.Size, &d.MTime, &d.Status, &d.Error,
			&d.ChunkCount, &d.PairID, &d.EmbedID, &d.AddedAt, &d.ProcessedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// InsertDocument adds a new pending document and queues it.
func InsertDocument(ctx context.Context, tx *sql.Tx, d Document) (int64, error) {
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `INSERT INTO documents (path, kind, sha256, size, mtime, status, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.Path, d.Kind, d.SHA256, d.Size, d.MTime, StatusPending, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, Enqueue(ctx, tx, id)
}

// MarkChanged records new content for a document and queues it again.
func MarkChanged(ctx context.Context, tx *sql.Tx, id int64, sha string, size, mtime int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET sha256 = ?, size = ?, mtime = ?, status = ?,
		error = NULL WHERE id = ?`, sha, size, mtime, StatusPending, id); err != nil {
		return err
	}
	return Enqueue(ctx, tx, id)
}

// Touch updates size/mtime when the content (hash) did not change.
func Touch(ctx context.Context, tx *sql.Tx, id, size, mtime int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE documents SET size = ?, mtime = ? WHERE id = ?`, size, mtime, id)
	return err
}

// Move gives a document a new path, keeping its chunks and embeddings (G10).
func Move(ctx context.Context, tx *sql.Tx, id int64, path string, size, mtime int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE documents SET path = ?, size = ?, mtime = ? WHERE id = ?`,
		path, size, mtime, id)
	return err
}

// SetStatus changes a document's status (used to revive stale documents).
func SetStatus(ctx context.Context, tx *sql.Tx, id int64, status string) error {
	_, err := tx.ExecContext(ctx, `UPDATE documents SET status = ? WHERE id = ?`, status, id)
	if err == nil && status == StatusPending {
		err = Enqueue(ctx, tx, id)
	}
	return err
}

// SetPair links a Markdown transcription and its PDF both ways (0 unlinks).
func SetPair(ctx context.Context, tx *sql.Tx, id, pairID int64) error {
	var v any
	if pairID != 0 {
		v = pairID
	}
	_, err := tx.ExecContext(ctx, `UPDATE documents SET pair_document_id = ? WHERE id = ?`, v, id)
	return err
}

// DeleteDocument removes a document with its chunks, BM25 postings, links
// and job, keeping term document frequencies correct.
func DeleteDocument(ctx context.Context, tx *sql.Tx, id int64) error {
	stmts := []string{
		`UPDATE terms SET df = df - (
			SELECT count(*) FROM postings p JOIN chunks c ON c.id = p.chunk_id
			WHERE p.term_id = terms.id AND c.document_id = ?1)
		WHERE id IN (SELECT p.term_id FROM postings p JOIN chunks c ON c.id = p.chunk_id WHERE c.document_id = ?1)`,
		`DELETE FROM postings WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id = ?1)`,
		`DELETE FROM chunks WHERE document_id = ?1`,
		// Images this document linked are indexed on their own again.
		`UPDATE documents SET status = 'pending' WHERE path IN (SELECT child_path FROM doc_links WHERE parent_id = ?1)`,
		`INSERT INTO jobs (document_id, state, attempts, enqueued_at)
			SELECT id, 'queued', 0, ?2 FROM documents WHERE path IN (SELECT child_path FROM doc_links WHERE parent_id = ?1)
			ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0`,
		`DELETE FROM doc_links WHERE parent_id = ?1`,
		`DELETE FROM jobs WHERE document_id = ?1`,
		// A PDF that loses its transcription needs its own text indexed.
		`INSERT INTO jobs (document_id, state, attempts, enqueued_at)
			SELECT id, 'queued', 0, ?2 FROM documents WHERE pair_document_id = ?1 AND kind = 'pdf'
			ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0`,
		`UPDATE documents SET status = 'pending' WHERE pair_document_id = ?1 AND kind = 'pdf'`,
		`UPDATE documents SET pair_document_id = NULL WHERE pair_document_id = ?1`,
		`DELETE FROM documents WHERE id = ?1`,
	}
	now := time.Now().Unix()
	for _, s := range stmts {
		args := []any{id}
		if strings.Contains(s, "?2") {
			args = append(args, now)
		}
		if _, err := tx.ExecContext(ctx, s, args...); err != nil {
			return fmt.Errorf("%w\n%s", err, s)
		}
	}
	return nil
}

// Enqueue (re)queues a document for indexing.
func Enqueue(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs (document_id, state, attempts, enqueued_at) VALUES (?, 'queued', 0, ?)
		ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0, last_error = NULL,
		enqueued_at = excluded.enqueued_at`, id, time.Now().Unix())
	return err
}

// RecoverInterrupted resets work left half-done by a crashed or killed
// indexer. Only the lock holder may call it.
func RecoverInterrupted(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `UPDATE documents SET status = ? WHERE status = ?`, StatusPending, StatusProcessing)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued' WHERE state = 'running'`)
	return n, err
}

// QueuedJobs counts jobs waiting to run.
func QueuedJobs(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE state = 'queued'`).Scan(&n)
	return n, err
}
