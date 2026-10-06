package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/satlavida/ragalay/internal/bm25"
	"github.com/satlavida/ragalay/internal/embed"
)

// ChunkRow is one chunk to store with its vector.
type ChunkRow struct {
	Modality    string
	Text        string
	HeadingPath string
	Page        int
	Tokens      int
	SourcePath  string // linked image file, when it is not the document itself
	Vector      []float32
}

// EmbedDim returns the dimension the index was built with.
func EmbedDim(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	v, err := Meta(ctx, q, "embed_dim")
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.Atoi(v)
}

// ReplaceChunks swaps a document's chunks, vectors and BM25 postings for new
// ones, keeping term document frequencies right. Vectors must have the
// index dimension (Turso does not check F32_BLOB sizes).
func ReplaceChunks(ctx context.Context, tx *sql.Tx, docID int64, rows []ChunkRow) error {
	dim, err := EmbedDim(ctx, tx)
	if err != nil {
		return err
	}
	for i, r := range rows {
		if len(r.Vector) != dim {
			return fmt.Errorf("chunk %d has a %d-dimension vector, the index uses %d", i, len(r.Vector), dim)
		}
	}
	if err := deleteChunks(ctx, tx, docID); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO chunks
		(document_id, ord, modality, text, heading_path, page, token_count, embedding, source_path, bm25_len)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	termIDs := map[string]int64{}
	for ord, r := range rows {
		tf := bm25.Counts(r.HeadingPath + " " + r.Text)
		n := 0
		for _, c := range tf {
			n += c
		}
		var page, source any
		if r.Page > 0 {
			page = r.Page
		}
		if r.SourcePath != "" {
			source = r.SourcePath
		}
		res, err := ins.ExecContext(ctx, docID, ord, r.Modality, r.Text, r.HeadingPath, page, r.Tokens,
			embed.Blob(r.Vector), source, n)
		if err != nil {
			return err
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for term, count := range tf {
			id, err := termID(ctx, tx, termIDs, term)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO postings (term_id, chunk_id, tf) VALUES (?, ?, ?)`,
				id, chunkID, count); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE terms SET df = df + 1 WHERE id = ?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func termID(ctx context.Context, tx *sql.Tx, cache map[string]int64, term string) (int64, error) {
	if id, ok := cache[term]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM terms WHERE term = ?`, term).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx, `INSERT INTO terms (term, df) VALUES (?, 0)`, term)
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	cache[term] = id
	return id, nil
}

func deleteChunks(ctx context.Context, tx *sql.Tx, docID int64) error {
	for _, s := range []string{
		`UPDATE terms SET df = df - (
			SELECT count(*) FROM postings p JOIN chunks c ON c.id = p.chunk_id
			WHERE p.term_id = terms.id AND c.document_id = ?1)
		WHERE id IN (SELECT p.term_id FROM postings p JOIN chunks c ON c.id = p.chunk_id WHERE c.document_id = ?1)`,
		`DELETE FROM postings WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id = ?1)`,
		`DELETE FROM chunks WHERE document_id = ?1`,
	} {
		if _, err := tx.ExecContext(ctx, s, docID); err != nil {
			return err
		}
	}
	return nil
}

// Job is a queued document ready to index.
type Job struct {
	Document
	Attempts   int
	PairedWith string // kind of the paired document ("markdown" for a transcribed PDF)
}

// MaxAttempts is how often a failing document is retried (once per run).
const MaxAttempts = 3

// QueuedJobsInOrder returns waiting jobs, text first so search is useful
// soon: Markdown, then PDFs, then images (plan1 §4.4).
func QueuedJobsInOrder(ctx context.Context, db *sql.DB) ([]Job, error) {
	rows, err := db.QueryContext(ctx, `SELECT d.id, d.path, d.kind, coalesce(d.sha256, ''), coalesce(d.size, 0),
		coalesce(d.mtime, 0), d.status, coalesce(d.error, ''), d.chunk_count, coalesce(d.pair_document_id, 0),
		coalesce(d.embed_id, ''), d.added_at, coalesce(d.processed_at, 0), j.attempts, coalesce(p.kind, '')
		FROM jobs j JOIN documents d ON d.id = j.document_id
		LEFT JOIN documents p ON p.id = d.pair_document_id
		WHERE j.state = 'queued'
		ORDER BY CASE d.kind WHEN 'markdown' THEN 0 WHEN 'pdf' THEN 1 ELSE 2 END, j.enqueued_at, d.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		d := &j.Document
		if err := rows.Scan(&d.ID, &d.Path, &d.Kind, &d.SHA256, &d.Size, &d.MTime, &d.Status, &d.Error,
			&d.ChunkCount, &d.PairID, &d.EmbedID, &d.AddedAt, &d.ProcessedAt, &j.Attempts, &j.PairedWith); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RequeueFailed gives failed jobs another try, up to MaxAttempts in total.
func RequeueFailed(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued' WHERE state = 'failed' AND attempts < ?`, MaxAttempts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkProcessing claims a document for indexing.
func MarkProcessing(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = ? WHERE id = ?`, StatusProcessing, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'running' WHERE document_id = ?`, id)
	return err
}

// MarkDone records a finished document and removes its job.
func MarkDone(ctx context.Context, tx *sql.Tx, id int64, chunks int, embedID, note string) error {
	var errText any
	if note != "" {
		errText = note
	}
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = ?, chunk_count = ?, embed_id = ?, error = ?,
		processed_at = ? WHERE id = ?`, StatusDone, chunks, embedID, errText, time.Now().Unix(), id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE document_id = ?`, id)
	return err
}

// MarkFailed records why a document could not be indexed.
func MarkFailed(ctx context.Context, tx *sql.Tx, id int64, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = ?, error = ?, processed_at = ? WHERE id = ?`,
		StatusFailed, reason, time.Now().Unix(), id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'failed', attempts = attempts + 1, last_error = ?
		WHERE document_id = ?`, reason, id)
	return err
}

// Requeue puts a claimed document back (indexing was interrupted).
func Requeue(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = ? WHERE id = ?`, StatusPending, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'queued' WHERE document_id = ?`, id)
	return err
}

// Link is an image linked from a Markdown document.
type Link struct {
	ChildPath, HeadingPath, Alt string
}

// ReplaceLinks stores a Markdown document's image links. Images it no longer
// links are re-queued so they get indexed on their own again; images it
// links now lose their own chunks (they are indexed with the document).
func ReplaceLinks(ctx context.Context, tx *sql.Tx, parentID int64, links []Link) error {
	keep := map[string]bool{}
	for _, l := range links {
		keep[l.ChildPath] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT child_path FROM doc_links WHERE parent_id = ?`, parentID)
	if err != nil {
		return err
	}
	var dropped []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if !keep[p] {
			dropped = append(dropped, p)
		}
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM doc_links WHERE parent_id = ?`, parentID); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `INSERT INTO doc_links (parent_id, child_path, heading_path, alt_text)
			VALUES (?, ?, ?, ?) ON CONFLICT (parent_id, child_path) DO NOTHING`,
			parentID, l.ChildPath, l.HeadingPath, l.Alt); err != nil {
			return err
		}
	}
	for _, p := range dropped {
		if linked, err := IsLinked(ctx, tx, p); err != nil || linked {
			if err != nil {
				return err
			}
			continue // still linked from another document
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, p).Scan(&id); err == nil {
			if err := SetStatus(ctx, tx, id, StatusPending); err != nil {
				return err
			}
		}
	}
	// Images now indexed with this document: drop their standalone chunks.
	for _, l := range links {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM documents WHERE path = ?`, l.ChildPath).Scan(&id); err != nil {
			continue
		}
		if err := deleteChunks(ctx, tx, id); err != nil {
			return err
		}
		embedID, _ := Meta(ctx, tx, "embed_id")
		if err := MarkDone(ctx, tx, id, 0, embedID, LinkedNote); err != nil {
			return err
		}
	}
	return nil
}

// LinkedNote marks an image that is indexed as part of a Markdown document.
const LinkedNote = "indexed with the Markdown file that shows it"

// IsLinked reports whether any Markdown document links the image at path.
func IsLinked(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, path string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM doc_links WHERE child_path = ?`, path).Scan(&n)
	return n > 0, err
}

// ResetForReembed empties the index for a new vector space: chunks are
// recreated at dim, BM25 data and the query cache are cleared, and every
// document is queued again. It is safe to interrupt: the new embed_id is
// recorded first, so the next run simply continues the queue.
func ResetForReembed(ctx context.Context, tx *sql.Tx, embedID string, dim int) (int64, error) {
	for _, s := range []string{
		`DROP TABLE IF EXISTS chunks`,
		chunksDDL(dim),
		`CREATE INDEX chunks_document ON chunks (document_id)`,
		`DELETE FROM postings`,
		`DELETE FROM terms`,
		`DELETE FROM query_cache`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return 0, fmt.Errorf("%w\n%s", err, s)
		}
	}
	if err := SetMeta(ctx, tx, "embed_id", embedID); err != nil {
		return 0, err
	}
	if err := SetMeta(ctx, tx, "embed_dim", strconv.Itoa(dim)); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE documents SET status = ?, chunk_count = 0, embed_id = NULL, error = NULL`,
		StatusPending)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs (document_id, state, attempts, enqueued_at)
		SELECT id, 'queued', 0, ? FROM documents WHERE true
		ON CONFLICT (document_id) DO UPDATE SET state = 'queued', attempts = 0, last_error = NULL`, now)
	return n, err
}
