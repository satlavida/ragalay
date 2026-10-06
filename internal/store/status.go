package store

import (
	"context"
	"database/sql"
	"strconv"
)

// Stats summarises the index for `ragalay status`.
type Stats struct {
	SchemaVersion int            `json:"schema_version"`
	EmbedID       string         `json:"embed_id"`
	EmbedDim      int            `json:"embed_dim"`
	Documents     int            `json:"documents"`
	ByStatus      map[string]int `json:"by_status"`
	ByKind        map[string]int `json:"by_kind"`
	Chunks        int            `json:"chunks"`
	SetupComplete bool           `json:"-"`
}

// ReadStats collects Stats in one pass.
func ReadStats(ctx context.Context, db *sql.DB) (Stats, error) {
	s := Stats{ByStatus: map[string]int{}, ByKind: map[string]int{}}
	var err error
	if s.SchemaVersion, err = Version(ctx, db); err != nil {
		return s, err
	}
	if s.EmbedID, err = Meta(ctx, db, "embed_id"); err != nil {
		return s, err
	}
	if dim, err := Meta(ctx, db, "embed_dim"); err != nil {
		return s, err
	} else if dim != "" {
		s.EmbedDim, _ = strconv.Atoi(dim)
	}
	accepted, err := Meta(ctx, db, "license_accepted_at")
	if err != nil {
		return s, err
	}
	s.SetupComplete = accepted != ""

	if err := countBy(ctx, db, `SELECT status, count(*) FROM documents GROUP BY status`, s.ByStatus); err != nil {
		return s, err
	}
	if err := countBy(ctx, db, `SELECT kind, count(*) FROM documents GROUP BY kind`, s.ByKind); err != nil {
		return s, err
	}
	for _, n := range s.ByStatus {
		s.Documents += n
	}
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&s.Chunks)
	return s, err
}

func countBy(ctx context.Context, db *sql.DB, q string, into map[string]int) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		into[k] = n
	}
	return rows.Err()
}

// CountUnder returns how many documents live under folder (stored form).
func CountUnder(ctx context.Context, db *sql.DB, folder string) (int, error) {
	var n int
	if folder == "." {
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&n)
		return n, err
	}
	prefix := folder + "/"
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM documents WHERE path = ? OR substr(path, 1, ?) = ?`,
		folder, len(prefix), prefix).Scan(&n)
	return n, err
}

// MarkStaleUnder marks every document under folder as stale; the next scan
// deletes them (unless they are still covered by another folder).
func MarkStaleUnder(ctx context.Context, tx *sql.Tx, folder string) (int64, error) {
	var res sql.Result
	var err error
	if folder == "." {
		res, err = tx.ExecContext(ctx, `UPDATE documents SET status = 'stale'`)
	} else {
		prefix := folder + "/"
		res, err = tx.ExecContext(ctx,
			`UPDATE documents SET status = 'stale' WHERE path = ? OR substr(path, 1, ?) = ?`,
			folder, len(prefix), prefix)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
