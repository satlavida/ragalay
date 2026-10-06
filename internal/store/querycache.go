package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// NormalizeQuery is the cache key form of a query: trimmed, with runs of
// whitespace collapsed. Case is kept because the embedding is case-sensitive
// (acronyms, names).
func NormalizeQuery(q string) string { return strings.Join(strings.Fields(q), " ") }

// CachedQuery returns the cached vector for (space, query) and records the
// hit. space identifies the vector space and query model (G3).
func CachedQuery(ctx context.Context, db *sql.DB, space, query string) ([]float32, bool, error) {
	var blob []byte
	err := db.QueryRowContext(ctx, `SELECT vector FROM query_cache WHERE embed_id = ? AND query_norm = ?`,
		space, query).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE query_cache SET hit_count = hit_count + 1, last_used = ?
		WHERE embed_id = ? AND query_norm = ?`, time.Now().Unix(), space, query); err != nil {
		return nil, false, err
	}
	v, err := embed.FromBlob(blob)
	return v, err == nil, err
}

// PutQuery stores a query vector.
func PutQuery(ctx context.Context, db *sql.DB, space, query string, v []float32) error {
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO query_cache (embed_id, query_norm, vector, hit_count, last_used, created_at)
		VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT (embed_id, query_norm) DO UPDATE SET vector = excluded.vector, last_used = excluded.last_used`,
		space, query, embed.Blob(v), now, now)
	return err
}

// EvictQueries drops entries unused for longer than ttl, then the least used
// (oldest first among equals) until at most max remain. ttl <= 0 disables
// expiry.
func EvictQueries(ctx context.Context, db *sql.DB, max int, ttl time.Duration) (int64, error) {
	var removed int64
	if ttl > 0 {
		res, err := db.ExecContext(ctx, `DELETE FROM query_cache WHERE last_used < ?`, time.Now().Add(-ttl).Unix())
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM query_cache`).Scan(&count); err != nil {
		return removed, err
	}
	if count > max {
		res, err := db.ExecContext(ctx, `DELETE FROM query_cache WHERE rowid IN (
			SELECT rowid FROM query_cache ORDER BY hit_count ASC, last_used ASC LIMIT ?)`, count-max)
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// ClearQueries empties the cache (model change, re-embed).
func ClearQueries(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `DELETE FROM query_cache`)
	return err
}
