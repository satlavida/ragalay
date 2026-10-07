package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/store"
)

// Spaces describes the index's vector spaces against the settings.
type Spaces struct {
	Live    string        // what search uses
	LiveDim int           //
	Next    string        // being built by a model switch, if any
	Want    string        // what the settings ask for
	Shadow  *store.Shadow // progress of the switch, if any
}

// Mismatch reports settings that name a space the index neither has nor
// is building: `ragalay reembed` (or `model use`) must start a switch.
func (s Spaces) Mismatch() bool { return s.Live != "" && s.Want != s.Live && s.Want != s.Next }

// ReadSpaces returns the index's spaces for cfg.
func ReadSpaces(ctx context.Context, root string, cfg config.Config) (Spaces, error) {
	sp := Spaces{Want: SpaceID(cfg)}
	err := store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var err error
		if sp.Live, err = store.Meta(ctx, db, store.MetaEmbedID); err != nil {
			return err
		}
		if sp.LiveDim, err = store.EmbedDim(ctx, db); err != nil {
			return err
		}
		if sp.Shadow, err = store.ReadShadow(ctx, db); err != nil {
			return err
		}
		if sp.Shadow != nil {
			sp.Next = sp.Shadow.To
		}
		return nil
	})
	return sp, err
}

// Switching reports whether a model switch is in progress (its last step,
// the swap, runs at the end of an indexing run even with an empty queue).
func Switching(ctx context.Context, root string) bool {
	building := false
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		t, err := store.WriteTarget(ctx, db)
		building = err == nil && t.Building
		return err
	})
	return building
}

// EmbedJSON is how the settings behind a space are recorded in the index.
func EmbedJSON(e config.Embed) string {
	e.IndexModel, e.IndexRevision, e.QueryModel = "", "", ""
	b, _ := json.Marshal(e)
	return string(b)
}

// LiveEmbed returns the settings that built the live index, so search keeps
// working while the settings name another model (plan2 S1). ok is false
// when they are unknown (an index from before Plan 2 whose settings have
// changed): search then has keywords only.
func LiveEmbed(ctx context.Context, root string, cfg config.Config) (emb config.Embed, ok bool, err error) {
	var live, recorded string
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		var err error
		if live, err = store.Meta(ctx, db, store.MetaEmbedID); err != nil {
			return err
		}
		recorded, err = store.Meta(ctx, db, store.MetaEmbedConfig)
		return err
	})
	if err != nil {
		return cfg.Embed, false, err
	}
	if live == "" || cfg.Embed.SpaceID() == live {
		return cfg.Embed, true, nil
	}
	if recorded != "" {
		var e config.Embed
		if json.Unmarshal([]byte(recorded), &e) == nil && e.SpaceID() == live {
			return e, true, nil
		}
	}
	// Indexes from before embed_config (Plan 1 folders): a local profile's
	// space names its model, revision and dim, so its settings follow.
	for _, p := range embed.Profiles() {
		for _, dim := range p.Dims {
			if p.Local() && embed.ID(p.IndexModel, p.IndexRevision, dim) == live {
				e := cfg.Embed
				e.Profile, e.Dim, e.OpenAI = p.Name, dim, nil
				return e, true, nil
			}
		}
	}
	return cfg.Embed, false, nil
}

// Reconcile applies settings that point back at the live model while a
// switch runs: the switch is cancelled (S3). Writers call it under the
// index lock. It returns the documents queued for the live index.
func Reconcile(ctx context.Context, root string, cfg config.Config) (cancelled bool, queued int64, err error) {
	sp, err := ReadSpaces(ctx, root, cfg)
	if err != nil || sp.Next == "" || sp.Want != sp.Live {
		return false, 0, err
	}
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			queued, err = store.CancelShadow(ctx, tx)
			return err
		})
	})
	return err == nil, queued, err
}

// Rebuild is the outcome of starting a re-embed.
type Rebuild struct {
	Queued  int64  `json:"queued"`
	Mode    string `json:"mode"` // "switch" (shadow build), "in_place" (nothing to keep), "continue", "cancelled", "none"
	From    string `json:"from"`
	To      string `json:"to"`
	NeedMB  int64  `json:"need_mb,omitempty"`
	FreeMB  int64  `json:"free_mb,omitempty"`
	Started bool   `json:"started"`
}

// ErrLowDisk means a model switch needs more free space (S4).
type ErrLowDisk struct{ NeedMB, FreeMB int64 }

func (e *ErrLowDisk) Error() string {
	return fmt.Sprintf("switching models needs about %d MB free next to the index while it rebuilds, but only %d MB is free; free some space or pass --force",
		e.NeedMB, e.FreeMB)
}

// StartRebuild starts re-embedding for cfg. With a populated index it builds
// the new space in the shadow tables so search keeps working (S1); a new
// choice during a switch restarts it (S3); the live model cancels it. force
// rebuilds even when nothing changed, and skips the disk check (S4).
// The caller holds the index lock and then runs the queue.
func StartRebuild(ctx context.Context, root string, cfg config.Config, force bool) (Rebuild, error) {
	sp, err := ReadSpaces(ctx, root, cfg)
	if err != nil {
		return Rebuild{}, err
	}
	rb := Rebuild{From: sp.Live, To: sp.Want, Mode: "none"}
	switch {
	case sp.Next != "" && sp.Want == sp.Live && !force:
		_, rb.Queued, err = Reconcile(ctx, root, cfg)
		rb.Mode = "cancelled"
		return rb, err
	case sp.Next != "" && sp.Want == sp.Next && !force:
		rb.Mode = "continue"
		return rb, nil
	case sp.Next == "" && sp.Want == sp.Live && !force:
		return rb, nil
	}
	var liveChunks int
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&liveChunks)
	})
	if err != nil {
		return rb, err
	}
	cfgJSON := EmbedJSON(cfg.Embed)
	if liveChunks == 0 {
		rb.Mode = "in_place"
		err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
			return store.Tx(ctx, db, func(tx *sql.Tx) error {
				var err error
				rb.Queued, err = store.ResetForReembed(ctx, tx, sp.Want, cfg.Embed.Dim, cfgJSON)
				return err
			})
		})
		rb.Started = err == nil
		return rb, err
	}
	rb.Mode = "switch"
	rb.NeedMB, rb.FreeMB = diskNeed(root, sp.LiveDim, cfg.Embed.Dim)
	if !force && rb.FreeMB >= 0 && float64(rb.FreeMB) < 1.5*float64(rb.NeedMB) {
		return rb, &ErrLowDisk{NeedMB: rb.NeedMB, FreeMB: rb.FreeMB}
	}
	err = store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			rb.Queued, err = store.StartShadow(ctx, tx, sp.Want, cfg.Embed.Dim, cfgJSON)
			return err
		})
	})
	rb.Started = err == nil
	return rb, err
}

// freeSpace is freeBytes, replaceable in tests.
var freeSpace = freeBytes

// diskNeed estimates the space a switch needs (the index again, scaled by
// the new vector size) and what is free, in MB. Free is -1 if unknown.
func diskNeed(root string, oldDim, newDim int) (needMB, freeMB int64) {
	var size int64
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(dbPath(root) + suffix); err == nil {
			size += fi.Size()
		}
	}
	if oldDim > 0 && newDim > oldDim {
		size = size * int64(newDim) / int64(oldDim)
	}
	free, err := freeSpace(root)
	if err != nil {
		return size/1e6 + 1, -1
	}
	return size/1e6 + 1, int64(free / 1e6)
}
