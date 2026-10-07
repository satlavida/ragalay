package index

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/store"
)

func liveVectorBytes(t *testing.T, root string) (chunks int, bytes int) {
	t.Helper()
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunks)
		var blob []byte
		db.QueryRowContext(ctx, `SELECT embedding FROM chunks WHERE embedding IS NOT NULL LIMIT 1`).Scan(&blob)
		bytes = len(blob)
		return nil
	})
	return chunks, bytes
}

// A model switch keeps the live index complete until the new one is done
// (plan2 S1), survives an interruption, and swaps atomically.
func TestModelSwitchKeepsSearchComplete(t *testing.T) {
	ctx := context.Background()
	root, cfg := mixedRoot(t)
	if _, err := runner(root, cfg, &fakeEmbedder{}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	liveChunks, _ := liveVectorBytes(t, root)

	old := cfg
	cfg.Embed.Dim = 512
	_, err := runner(root, cfg, &fakeEmbedder{}).Run(ctx)
	var mm *ErrModelMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Config, ":512") || mm.Documents != 8 {
		t.Fatalf("want model mismatch, got %v", err)
	}
	// Search keeps the settings that built the live index.
	if e, ok, _ := LiveEmbed(ctx, root, cfg); !ok || e.Dim != 1024 {
		t.Fatalf("live embed %+v %v", e, ok)
	}

	rb, err := StartRebuild(ctx, root, cfg, false)
	if err != nil || rb.Mode != "switch" || rb.Queued != 8 || !rb.Started {
		t.Fatalf("start: %+v %v", rb, err)
	}
	// Interrupted halfway: search still sees every live chunk at 1024 dims.
	f := &fakeEmbedder{block: make(chan struct{})}
	rctx, cancel := context.WithCancel(ctx)
	go func() {
		for f.calls.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := runner(root, cfg, f).Run(rctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted switch: %v", err)
	}
	if n, b := liveVectorBytes(t, root); n != liveChunks || b != 1024*4 {
		t.Fatalf("live index changed during the switch: %d chunks, %d bytes", n, b)
	}
	sp, _ := ReadSpaces(ctx, root, cfg)
	if sp.Shadow == nil || sp.Shadow.Done == 0 || sp.Shadow.Done >= sp.Shadow.Total || sp.Mismatch() {
		t.Fatalf("switch progress %+v", sp.Shadow)
	}
	// Running again continues the switch and swaps it in.
	if rb, err := StartRebuild(ctx, root, cfg, false); err != nil || rb.Mode != "continue" {
		t.Fatalf("continue: %+v %v", rb, err)
	}
	sum, err := runner(root, cfg, &fakeEmbedder{}).Run(ctx)
	if err != nil || !sum.Switched {
		t.Fatalf("finish: %+v %v", sum, err)
	}
	if n, b := liveVectorBytes(t, root); n != liveChunks || b != 512*4 {
		t.Fatalf("after switch: %d chunks, %d bytes", n, b)
	}
	if sp, _ := ReadSpaces(ctx, root, cfg); sp.Live != SpaceID(cfg) || sp.Shadow != nil || sp.Mismatch() {
		t.Fatalf("spaces after switch %+v", sp)
	}

	// Switching back and changing your mind cancels; only real changes are
	// redone in the live index (S3).
	if rb, err := StartRebuild(ctx, root, old, false); err != nil || rb.Mode != "switch" {
		t.Fatalf("switch back: %+v %v", rb, err)
	}
	if cancelled, queued, err := Reconcile(ctx, root, cfg); err != nil || !cancelled || queued > 2 {
		t.Fatalf("cancel: %v %d %v", cancelled, queued, err)
	}
	if n, b := liveVectorBytes(t, root); n != liveChunks || b != 512*4 {
		t.Fatalf("after cancel: %d chunks, %d bytes", n, b)
	}
}

func TestSwitchChecksFreeSpace(t *testing.T) {
	ctx := context.Background()
	root, cfg := mixedRoot(t)
	if _, err := runner(root, cfg, &fakeEmbedder{}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer func(f func(string) (uint64, error)) { freeSpace = f }(freeSpace)
	freeSpace = func(string) (uint64, error) { return 0, nil }
	cfg.Embed.Dim = 512
	_, err := StartRebuild(ctx, root, cfg, false)
	var low *ErrLowDisk
	if !errors.As(err, &low) || Switching(ctx, root) {
		t.Fatalf("want low-disk refusal and no switch, got %v", err)
	}
	if rb, err := StartRebuild(ctx, root, cfg, true); err != nil || rb.Mode != "switch" {
		t.Fatalf("--force must override: %+v %v", rb, err)
	}
}

// A Plan 1 index has no recorded settings; search still knows its model.
func TestLiveEmbedOfPlan1Index(t *testing.T) {
	ctx := context.Background()
	root, cfg := mixedRoot(t) // jina-v5 at 1024
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			return store.SetLiveSpace(ctx, tx, SpaceID(cfg), 1024, "")
		})
	})
	gemma := cfg
	gemma.Embed, _ = gemma.Embed.UseProfile("embeddinggemma-2", 0)
	e, ok, err := LiveEmbed(ctx, root, gemma)
	if err != nil || !ok || e.Profile != "jina-v5" || e.Dim != 1024 {
		t.Fatalf("live embed %+v %v %v", e, ok, err)
	}
}

func TestFreshIndexRebuildsInPlace(t *testing.T) {
	ctx := context.Background()
	root, cfg := mixedRoot(t) // scanned, nothing embedded yet
	CheckSpace(ctx, root, cfg)
	cfg.Embed.Dim = 256
	rb, err := StartRebuild(ctx, root, cfg, false)
	if err != nil || rb.Mode != "in_place" || rb.Queued != 8 {
		t.Fatalf("%+v %v", rb, err)
	}
	if _, err := runner(root, cfg, &fakeEmbedder{}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, b := liveVectorBytes(t, root); b != 256*4 {
		t.Fatalf("vectors %d bytes", b)
	}
}
