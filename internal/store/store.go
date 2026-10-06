// Package store owns the Turso database in .ragalay/index.db.
//
// Turso locks the database file per process on Windows, so connections are
// short-lived: open, do the work, close (plan1 §3.1). With retries on lock
// errors, other processes can read while an indexer is running.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "turso.tech/database/tursogo"
)

// LockWait is how long With keeps retrying while another process holds the
// database.
var LockWait = 5 * time.Second

// ErrBusy means another process kept the database locked for longer than
// LockWait.
var ErrBusy = errors.New("database is busy (another ragalay process is indexing); try again shortly")

// IsLocked reports whether err comes from another process holding the file.
func IsLocked(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Locking error") || strings.Contains(s, "database is locked") ||
		strings.Contains(s, "Database is busy")
}

// With opens the database at path, runs fn and closes it again. If the file
// is locked by another process, the whole call is retried with backoff, so fn
// must be safe to repeat (do writes inside a transaction).
func With(ctx context.Context, path string, fn func(*sql.DB) error) error {
	deadline := time.Now().Add(LockWait)
	delay := 25 * time.Millisecond
	for {
		err := withOnce(ctx, path, fn)
		if !IsLocked(err) {
			return err
		}
		if time.Now().Add(delay).After(deadline) {
			return ErrBusy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

func withOnce(ctx context.Context, path string, fn func(*sql.DB) error) error {
	db, err := sql.Open("turso", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	return fn(db)
}

// Checkpoint moves the write-ahead log into index.db, so the folder can be
// copied or backed up as just its files without losing recent writes. Best
// effort: errors are ignored.
func Checkpoint(ctx context.Context, path string) {
	With(ctx, path, func(db *sql.DB) error {
		var busy, logPages, done int
		return db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &done)
	})
}

// Tx runs fn in a transaction, committing on success.
func Tx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Meta reads one value from the meta table. Missing keys return "".
func Meta(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes one value to the meta table.
func SetMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set meta %s: %w", key, err)
	}
	return nil
}
