package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestQueryCache(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	err := With(ctx, path, func(db *sql.DB) error {
		if got := NormalizeQuery("  multi-head \t attention\n"); got != "multi-head attention" {
			t.Errorf("NormalizeQuery = %q", got)
		}
		if _, ok, err := CachedQuery(ctx, db, "s", "q"); ok || err != nil {
			t.Fatalf("empty cache hit: %v %v", ok, err)
		}
		if err := PutQuery(ctx, db, "s", "q", []float32{0.6, 0.8}); err != nil {
			return err
		}
		v, ok, err := CachedQuery(ctx, db, "s", "q")
		if err != nil || !ok || len(v) != 2 || v[1] != 0.8 {
			t.Fatalf("cache miss after put: %v %v %v", v, ok, err)
		}
		if _, ok, _ := CachedQuery(ctx, db, "other-space", "q"); ok {
			t.Fatal("cache must be keyed by vector space")
		}

		// Eviction: "q" has 2 hits; add two single-hit entries, oldest first.
		PutQuery(ctx, db, "s", "old", []float32{1})
		db.ExecContext(ctx, `UPDATE query_cache SET last_used = last_used - 100 WHERE query_norm = 'old'`)
		PutQuery(ctx, db, "s", "new", []float32{1})
		n, err := EvictQueries(ctx, db, 2, 0)
		if err != nil || n != 1 {
			t.Fatalf("evict to 2: removed %d, %v", n, err)
		}
		if _, ok, _ := CachedQuery(ctx, db, "s", "old"); ok {
			t.Fatal("least-used oldest entry should be evicted first")
		}
		if _, ok, _ := CachedQuery(ctx, db, "s", "q"); !ok {
			t.Fatal("most-used entry should survive")
		}

		// TTL expiry.
		db.ExecContext(ctx, `UPDATE query_cache SET last_used = last_used - 7200 WHERE query_norm = 'new'`)
		if n, err := EvictQueries(ctx, db, 100, time.Hour); err != nil || n != 1 {
			t.Fatalf("ttl evict: removed %d, %v", n, err)
		}
		if err := ClearQueries(ctx, db); err != nil {
			return err
		}
		var c int
		db.QueryRowContext(ctx, `SELECT count(*) FROM query_cache`).Scan(&c)
		if c != 0 {
			t.Fatalf("clear left %d rows", c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
