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
	"path/filepath"
	"strconv"
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
func SpaceID(cfg config.Config) string {
	return embed.ID(cfg.Embed.IndexModel, cfg.Embed.IndexRevision, cfg.Embed.Dim)
}

// CheckSpace compares the index with the config. A fresh index (no embed_id
// yet) adopts the config's space.
func CheckSpace(ctx context.Context, root string, cfg config.Config) error {
	want := SpaceID(cfg)
	return store.With(ctx, dbPath(root), func(db *sql.DB) error {
		have, err := store.Meta(ctx, db, "embed_id")
		if err != nil {
			return err
		}
		dim, err := store.EmbedDim(ctx, db)
		if err != nil {
			return err
		}
		if have == "" && dim == cfg.Embed.Dim {
			return store.Tx(ctx, db, func(tx *sql.Tx) error { return store.SetMeta(ctx, tx, "embed_id", want) })
		}
		if have != want {
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
}

// Summary is the result of a run.
type Summary struct {
	Indexed  int           `json:"indexed"`
	Failed   int           `json:"failed"`
	Linked   int           `json:"linked"` // images indexed as part of a Markdown file
	Chunks   int           `json:"chunks"`
	Requeued int64         `json:"requeued"`
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
func (r *Runner) Run(ctx context.Context) (Summary, error) {
	start := time.Now()
	sum := Summary{Errors: []DocError{}}
	if err := CheckSpace(ctx, r.Root, r.Cfg); err != nil {
		return sum, err
	}
	space := SpaceID(r.Cfg)

	var jobs []store.Job
	err := store.With(ctx, dbPath(r.Root), func(db *sql.DB) error {
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
	eta := newETA(r.Root)
	defer func() {
		eta.save(r.Root)
		// Fold the write-ahead log into index.db so the folder copies cleanly.
		store.Checkpoint(context.Background(), dbPath(r.Root))
	}()

	for i, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		if r.Progress != nil {
			r.Progress(Progress{Done: sum.Indexed + sum.Linked, Failed: sum.Failed, Total: len(jobs),
				Current: job.Path, Elapsed: time.Since(start), ETA: eta.remaining(jobs[i:])})
		}
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
				continue
			}
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
			}
			rows, err = r.embed(ctx, emb, res.Units)
		}
		if ctx.Err() != nil {
			r.release(job.ID) // interrupted: try again next time
			break
		}
		if err != nil {
			sum.Failed++
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
		eta.observe(job.Kind, time.Since(docStart))
		r.logf("indexed %s: %d chunks in %v", job.Path, len(rows), time.Since(docStart).Round(time.Millisecond))
	}
	sum.Duration = time.Since(start)
	if r.Progress != nil {
		r.Progress(Progress{Done: sum.Indexed + sum.Linked, Failed: sum.Failed, Total: len(jobs), Elapsed: sum.Duration})
	}
	if ctx.Err() != nil {
		return sum, ctx.Err()
	}
	return sum, nil
}

// embed embeds units in batches and fits vectors to the index dimension.
func (r *Runner) embed(ctx context.Context, emb Embedder, units []extract.Unit) ([]store.ChunkRow, error) {
	batch := r.BatchSize
	if batch <= 0 {
		batch = 16
	}
	rows := make([]store.ChunkRow, len(units))
	for i := 0; i < len(units); i += batch {
		end := min(i+batch, len(units))
		in := make([]embed.Input, end-i)
		for j, u := range units[i:end] {
			in[j] = u.Input(r.Root)
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
			u := units[i+j]
			row := store.ChunkRow{Modality: u.Modality, Text: u.Text, HeadingPath: u.HeadingPath, Page: u.Page,
				Tokens: u.Tokens, Vector: fit}
			if u.Modality == embed.Image {
				row.SourcePath = u.Source
			}
			rows[i+j] = row
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

// eta keeps running averages of seconds per document kind, persisted in
// meta so the first estimate of the next run is already informed.
type eta struct {
	avg   map[string]float64
	count map[string]int
}

func newETA(root string) *eta {
	e := &eta{avg: map[string]float64{}, count: map[string]int{}}
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		for _, k := range []string{scan.KindMarkdown, scan.KindPDF, scan.KindImage} {
			if v, _ := store.Meta(ctx, db, "eta_"+k); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					e.avg[k], e.count[k] = f, 1
				}
			}
		}
		return nil
	})
	return e
}

func (e *eta) observe(kind string, d time.Duration) {
	n := min(e.count[kind], 19) // moving average over ~20 documents
	e.avg[kind] = (e.avg[kind]*float64(n) + d.Seconds()) / float64(n+1)
	e.count[kind]++
}

func (e *eta) remaining(jobs []store.Job) time.Duration {
	var s float64
	for _, j := range jobs {
		avg, ok := e.avg[j.Kind]
		if !ok {
			avg = map[string]float64{scan.KindMarkdown: 1, scan.KindPDF: 10, scan.KindImage: 1}[j.Kind]
		}
		s += avg
	}
	return time.Duration(s * float64(time.Second))
}

func (e *eta) save(root string) {
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			for k, v := range e.avg {
				if err := store.SetMeta(ctx, tx, "eta_"+k, strconv.FormatFloat(v, 'f', 3, 64)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// Reembed resets the index for the config's vector space and returns how
// many documents were queued. Run then rebuilds it; an interrupted rebuild
// continues on the next run.
func Reembed(ctx context.Context, root string, cfg config.Config) (int64, error) {
	var n int64
	err := store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			n, err = store.ResetForReembed(ctx, tx, SpaceID(cfg), cfg.Embed.Dim)
			return err
		})
	})
	return n, err
}

// IsMismatch reports whether err is a model mismatch.
func IsMismatch(err error) bool {
	var m *ErrModelMismatch
	return errors.As(err, &m)
}
