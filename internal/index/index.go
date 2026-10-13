// Package index runs the indexing queue: extract each pending document,
// embed it with the omni sidecar, and store chunks, vectors and BM25
// postings in one transaction per document (plan1 §4.4, Phase 5).
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/extract"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/store"
)

// ErrModelMismatch means the index was built in a different vector space
// than the config asks for; `ragalay reembed` fixes it (plan1 §4.6).
type ErrModelMismatch struct {
	Index, Config string
	Documents     int
}

func (e *ErrModelMismatch) Error() string {
	return fmt.Sprintf("the index was built with %s, but the settings now say %s; run \"ragalay reembed\" to rebuild it (%d documents)",
		e.Index, e.Config, e.Documents)
}

// SpaceID is the embed_id the config asks for.
func SpaceID(cfg config.Config) string { return cfg.Embed.SpaceID() }

// CheckSpace compares the index with the config. A fresh index (no embed_id
// yet) adopts the config's space. Settings that name the space a model
// switch is building are fine: indexing continues the switch.
func CheckSpace(ctx context.Context, root string, cfg config.Config) error {
	want := SpaceID(cfg)
	return store.With(ctx, dbPath(root), func(db *sql.DB) error {
		have, err := store.Meta(ctx, db, store.MetaEmbedID)
		if err != nil {
			return err
		}
		next, err := store.Meta(ctx, db, store.MetaNextEmbedID)
		if err != nil {
			return err
		}
		dim, err := store.EmbedDim(ctx, db)
		if err != nil {
			return err
		}
		if have == "" && dim == cfg.Embed.Dim {
			return store.Tx(ctx, db, func(tx *sql.Tx) error {
				return store.SetLiveSpace(ctx, tx, want, dim, EmbedJSON(cfg.Embed))
			})
		}
		if have == "" {
			// Nothing was ever embedded (the model changed between init and
			// the first scan): recreate the empty tables at the new size.
			var chunks int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunks); err != nil {
				return err
			}
			if chunks == 0 {
				return store.Tx(ctx, db, func(tx *sql.Tx) error {
					_, err := store.ResetForReembed(ctx, tx, want, cfg.Embed.Dim, EmbedJSON(cfg.Embed))
					return err
				})
			}
		}
		if have != want && next != want {
			var n int
			db.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&n)
			return &ErrModelMismatch{Index: have, Config: want, Documents: n}
		}
		return nil
	})
}

func dbPath(root string) string { return filepath.Join(root, config.DirName, config.DBFile) }

// Embedder is what the runner needs from the indexing model.
type Embedder interface {
	EmbedDocuments(ctx context.Context, in []embed.Input) ([][]float32, error)
	Close() error
}

// Runner indexes the queue. The caller must hold the index lock.
type Runner struct {
	Root string
	Cfg  config.Config
	// NewEmbedder starts the indexing model. It is called only when there is
	// work, so an empty queue never starts Python.
	NewEmbedder func(ctx context.Context) (Embedder, error)
	Tokenizer   chunk.Tokenizer // nil: estimate
	Log         io.Writer
	Progress    func(Progress)
	BatchSize   int // inputs per embedding call (default 16)
}

// Progress is reported after each document.
type Progress struct {
	Done, Failed, Total int
	Current             string        // document being indexed
	Elapsed             time.Duration // since the run started
	ETA                 time.Duration // estimate for the rest of the queue
	Percent             float64       // 0-100 of the queue's work, by size not file count
	Estimating          bool          // ETA is a first guess: too little measured yet
}

// Summary is the result of a run.
type Summary struct {
	Indexed  int           `json:"indexed"`
	Failed   int           `json:"failed"`
	Linked   int           `json:"linked"`            // images indexed as part of a Markdown file
	Skipped  int           `json:"skipped,omitempty"` // audio or video the model cannot read
	Chunks   int           `json:"chunks"`
	Requeued int64         `json:"requeued"`
	Switched bool          `json:"switched,omitempty"` // a model switch finished and search now uses it
	Duration time.Duration `json:"duration_ns"`
	Device   string        `json:"device,omitempty"`
	Errors   []DocError    `json:"errors"`
}

// DocError is a document that failed.
type DocError struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Run indexes queued documents until the queue is empty or ctx ends. An
// interrupted document goes back to the queue.
func (r *Runner) Run(ctx context.Context) (sum Summary, err error) {
	start := time.Now()
	sum = Summary{Errors: []DocError{}}
	if _, _, err := Reconcile(ctx, r.Root, r.Cfg); err != nil {
		return sum, err
	}
	if err := CheckSpace(ctx, r.Root, r.Cfg); err != nil {
		return sum, err
	}
	space := SpaceID(r.Cfg)
	defer func() {
		if ctx.Err() == nil {
			sum.Switched = r.finishSwitch()
		}
	}()

	var jobs []store.Job
	err = store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		err := store.Tx(ctx, db, func(tx *sql.Tx) error {
			if _, err := store.RecoverInterrupted(ctx, tx); err != nil {
				return err
			}
			var err error
			sum.Requeued, err = store.RequeueFailed(ctx, tx)
			return err
		})
		if err != nil {
			return err
		}
		jobs, err = store.QueuedJobsInOrder(ctx, db)
		return err
	})
	if err != nil || len(jobs) == 0 {
		return sum, err
	}

	tok := r.Tokenizer
	if tok == nil {
		tok = chunk.Estimate{}
	}
	ex := extract.New(r.Root, extract.Options{Tokens: r.Cfg.Chunk.Tokens, Overlap: r.Cfg.Chunk.Overlap,
		PageImages: r.Cfg.Index.PDFPageImages, Tokenizer: tok})
	defer ex.Close()

	var emb Embedder
	defer func() {
		if emb != nil {
			emb.Close()
		}
	}()
	eta := newRates(r.Root)
	var totalUnits, doneUnits float64
	for _, j := range jobs {
		totalUnits += cost(j)
	}
	// report sends progress; partial is the fraction of jobs[i] already done.
	report := func(i int, partial float64) {
		if r.Progress == nil {
			return
		}
		pct := 100 * (doneUnits + cost(jobs[i])*partial) / totalUnits
		r.Progress(Progress{Done: sum.Indexed + sum.Linked, Failed: sum.Failed, Total: len(jobs),
			Current: jobs[i].Path, Elapsed: time.Since(start), ETA: eta.remaining(jobs[i:], partial),
			Percent: min(pct, 100), Estimating: eta.estimating(jobs[i:])})
	}
	defer func() {
		eta.save(r.Root)
		// Fold the write-ahead log into index.db so the folder copies cleanly.
		store.Checkpoint(context.Background(), dbPath(r.Root))
	}()

	for i, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		report(i, 0)
		docStart := time.Now()
		// Images shown in a Markdown file are indexed with it.
		if job.Kind == scan.KindImage {
			linked := false
			store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
				var err error
				linked, err = store.IsLinked(ctx, db, job.Path)
				return err
			})
			if linked {
				if err := r.finish(ctx, job, nil, nil, space, store.LinkedNote); err != nil {
					return sum, err
				}
				sum.Linked++
				doneUnits += cost(job)
				continue
			}
		}
		// A model without an audio or video tower cannot index media. The file
		// is done with a note, and a model switch queues it again.
		if need := mediaModality(job.Kind); need != "" && !slices.Contains(r.Cfg.Embed.Modalities(), need) {
			note := fmt.Sprintf("skipped: this model cannot read %s; switch to EmbeddingGemma 2 (ragalay model use embeddinggemma-2)", need)
			if err := r.finish(ctx, job, nil, nil, space, note); err != nil {
				return sum, err
			}
			sum.Skipped++
			doneUnits += cost(job)
			continue
		}
		if err := r.claim(ctx, job.ID); err != nil {
			return sum, err
		}
		res, err := ex.Extract(ctx, job.Kind, job.Path, job.Kind == scan.KindPDF && job.PairedWith == scan.KindMarkdown)
		var rows []store.ChunkRow
		if err == nil && len(res.Units) > 0 {
			if emb == nil {
				if emb, err = r.NewEmbedder(ctx); err != nil {
					r.release(job.ID)
					return sum, fmt.Errorf("start indexing model: %w", err)
				}
				docStart = time.Now() // model start-up is not document time
			}
			rows, err = r.embed(ctx, emb, job.Path, res.Units, func(f float64) { report(i, f) })
		}
		if ctx.Err() != nil {
			r.release(job.ID) // interrupted: try again next time
			break
		}
		if err != nil {
			sum.Failed++
			doneUnits += cost(job)
			sum.Errors = append(sum.Errors, DocError{Path: job.Path, Error: err.Error()})
			r.logf("failed %s: %v", job.Path, err)
			if ferr := r.fail(ctx, job.ID, err.Error()); ferr != nil {
				return sum, ferr
			}
			continue
		}
		var links []store.Link
		for _, l := range res.Links {
			links = append(links, store.Link{ChildPath: l.Path, HeadingPath: l.HeadingPath, Alt: l.Alt})
		}
		note := ""
		if len(res.Warnings) > 0 {
			note = fmt.Sprintf("%d warnings: %s", len(res.Warnings), res.Warnings[0])
		}
		if job.Kind == scan.KindPDF && job.PairedWith == scan.KindMarkdown {
			note = "text indexed from its Markdown transcription"
		}
		if err := r.finish(ctx, job, rows, links, space, note); err != nil {
			return sum, err
		}
		sum.Indexed++
		sum.Chunks += len(rows)
		eta.observe(job.Kind, cost(job), time.Since(docStart))
		doneUnits += cost(job)
		r.logf("indexed %s: %d chunks in %v", job.Path, len(rows), time.Since(docStart).Round(time.Millisecond))
	}
	sum.Duration = time.Since(start)
	if r.Progress != nil {
		r.Progress(Progress{Done: sum.Indexed + sum.Linked, Failed: sum.Failed, Total: len(jobs), Elapsed: sum.Duration, Percent: 100})
	}
	if ctx.Err() != nil {
		return sum, ctx.Err()
	}
	return sum, nil
}

// mediaModality is the modality a media document needs, "" for other kinds.
func mediaModality(kind string) string {
	switch kind {
	case scan.KindAudio:
		return embed.Audio
	case scan.KindVideo:
		return embed.Video
	}
	return ""
}

// finishSwitch swaps a finished model switch in (S1). It reports whether it
// did; a failure leaves the switch in place for the next run.
func (r *Runner) finishSwitch() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switched := false
	err := store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		t, err := store.WriteTarget(ctx, db)
		if err != nil || !t.Building {
			return err
		}
		if done, err := store.ShadowComplete(ctx, db); err != nil || !done {
			return err
		}
		if err := store.Tx(ctx, db, func(tx *sql.Tx) error { return store.FinishShadow(ctx, tx) }); err != nil {
			return err
		}
		switched = true
		r.logf("model switch finished: search now uses %s", t.EmbedID)
		return nil
	})
	if err != nil {
		r.logf("finishing the model switch: %v", err)
	}
	return switched
}

// embed embeds units in batches and fits vectors to the index dimension.
func (r *Runner) embed(ctx context.Context, emb Embedder, docPath string, units []extract.Unit, onBatch func(float64)) ([]store.ChunkRow, error) {
	batch := r.BatchSize
	if batch <= 0 {
		batch = 16
	}
	// Units the model cannot embed (plan2 §5.4): page images are dropped
	// (the page text is indexed), other images stay keyword-only.
	can := r.Cfg.Embed.Modalities()
	var keep []extract.Unit
	for _, u := range units {
		if u.Modality == embed.PDFPage && !slices.Contains(can, embed.PDFPage) {
			continue
		}
		keep = append(keep, u)
	}
	units = keep
	rows := make([]store.ChunkRow, len(units))
	for i, u := range units {
		rows[i] = store.ChunkRow{Modality: u.Modality, Text: u.Text, HeadingPath: u.HeadingPath, Page: u.Page, Tokens: u.Tokens}
		if u.Modality == embed.Image {
			rows[i].SourcePath = u.Source
			if rows[i].Text == "" {
				// Keyword search can still find an image by its name.
				rows[i].Text = strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSuffix(path.Base(u.Source), path.Ext(u.Source)))
			}
		}
	}
	var todo []int
	for i, u := range units {
		if slices.Contains(can, u.Modality) {
			todo = append(todo, i)
		}
	}
	for b := 0; b < len(todo); b += batch {
		idx := todo[b:min(b+batch, len(todo))]
		in := make([]embed.Input, len(idx))
		for j, i := range idx {
			in[j] = units[i].Input(r.Root, docPath)
		}
		vecs, err := emb.EmbedDocuments(ctx, in)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(in) {
			return nil, fmt.Errorf("asked for %d vectors, got %d", len(in), len(vecs))
		}
		for j, v := range vecs {
			fit, err := embed.Fit(v, r.Cfg.Embed.Dim)
			if err != nil {
				return nil, err
			}
			rows[idx[j]].Vector = fit
		}
		if onBatch != nil {
			onBatch(float64(b+len(idx)) / float64(len(todo)))
		}
	}
	return rows, nil
}

func (r *Runner) claim(ctx context.Context, id int64) error {
	return store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error { return store.MarkProcessing(ctx, tx, id) })
	})
}

// release puts a document back in the queue; it uses a fresh context because
// the run's context may be cancelled already.
func (r *Runner) release(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error { return store.Requeue(ctx, tx, id) })
	})
}

func (r *Runner) fail(ctx context.Context, id int64, reason string) error {
	return store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error { return store.MarkFailed(ctx, tx, id, reason) })
	})
}

// finish stores a document's chunks, links and status in one transaction.
func (r *Runner) finish(ctx context.Context, job store.Job, rows []store.ChunkRow, links []store.Link, space, note string) error {
	return store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			if err := store.ReplaceChunks(ctx, tx, job.ID, rows); err != nil {
				return err
			}
			if job.Kind == scan.KindMarkdown {
				if err := store.ReplaceLinks(ctx, tx, job.ID, links); err != nil {
					return err
				}
			}
			return store.MarkDone(ctx, tx, job.ID, len(rows), space, note)
		})
	})
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", args...)
	}
}

// IsMismatch reports whether err is a model mismatch.
func IsMismatch(err error) bool {
	var m *ErrModelMismatch
	return errors.As(err, &m)
}
