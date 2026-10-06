package scan

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/download"
	"github.com/satlavida/ragalay/internal/store"
)

// Report says what a scan changed. The JSON form is part of `scan --json`.
type Report struct {
	Found     int        `json:"found"`
	New       []string   `json:"new"`
	Changed   []string   `json:"changed"`
	Moved     []Move     `json:"moved"`
	Deleted   []string   `json:"deleted"`
	Revived   []string   `json:"revived"` // stale documents that are back in scope
	Unchanged int        `json:"unchanged"`
	Pairs     []PairInfo `json:"pairs"`
	Warnings  []string   `json:"warnings"`
	Missing   []string   `json:"missing_folders"`
	Recovered int64      `json:"recovered"` // documents reset after an interrupted run
	Queued    int        `json:"queued"`    // documents waiting to be indexed
}

// Move is a renamed or moved file whose embeddings were kept.
type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PairInfo is a Markdown ↔ PDF link.
type PairInfo struct {
	MD  string `json:"md"`
	PDF string `json:"pdf"`
	Via string `json:"via"`
}

// Changes reports whether the scan modified the index.
func (r Report) Changes() bool {
	return len(r.New)+len(r.Changed)+len(r.Moved)+len(r.Deleted)+len(r.Revived) > 0 || r.Recovered > 0
}

// Run scans the configured folders and updates the documents table. The
// caller must hold the index lock. Files are hashed with the database
// closed; all changes are written in one short transaction.
func Run(ctx context.Context, root string, cfg config.Config) (Report, error) {
	rep := Report{New: []string{}, Changed: []string{}, Moved: []Move{}, Deleted: []string{},
		Revived: []string{}, Pairs: []PairInfo{}, Warnings: []string{}, Missing: []string{}}
	dbPath := filepath.Join(root, config.DirName, config.DBFile)

	files, missing, err := Walk(root, cfg)
	if err != nil {
		return rep, err
	}
	rep.Found = len(files)
	if missing != nil {
		rep.Missing = missing
	}

	var existing []store.Document
	if err := store.With(ctx, dbPath, func(db *sql.DB) error {
		existing, err = store.ListDocuments(ctx, db, "", "")
		return err
	}); err != nil {
		return rep, err
	}
	byPath := make(map[string]store.Document, len(existing))
	for _, d := range existing {
		byPath[d.Path] = d
	}

	// Classify. Hashing is the slow part, so only files whose size or mtime
	// changed (or that are new) are hashed.
	type change struct {
		doc  store.Document
		file File
		sha  string
	}
	var newFiles []change
	var changed, touched, revived []change
	for _, f := range sortedFiles(files) {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		d, known := byPath[f.Path]
		if known && d.Size == f.Size && d.MTime == f.MTime && d.SHA256 != "" {
			if d.Status == store.StatusStale {
				revived = append(revived, change{doc: d, file: f, sha: d.SHA256})
			} else {
				rep.Unchanged++
			}
			continue
		}
		sha, err := download.SHA256(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		switch {
		case !known:
			newFiles = append(newFiles, change{file: f, sha: sha})
		case sha == d.SHA256 && d.Status == store.StatusStale:
			revived = append(revived, change{doc: d, file: f, sha: sha})
		case sha == d.SHA256:
			touched = append(touched, change{doc: d, file: f, sha: sha})
		default:
			changed = append(changed, change{doc: d, file: f, sha: sha})
		}
	}

	// Documents to drop: anything not found in the scanned folders (deleted,
	// ignored, of a kind that was turned off, or in a folder no longer
	// listed). The exception is folders in scan.keep ("folders remove
	// --keep"): their documents stay while the files exist.
	var gone []store.Document
	for _, d := range existing {
		if _, ok := files[d.Path]; ok {
			continue
		}
		kept := slices.ContainsFunc(cfg.Scan.Keep, func(f string) bool { return config.Covers(f, d.Path) })
		if kept && d.Status != store.StatusStale && fileExists(filepath.Join(root, filepath.FromSlash(d.Path))) {
			rep.Unchanged++
			continue
		}
		gone = append(gone, d)
	}

	// Moves: a new file with the same content as a vanished one (G10).
	var moves []change
	goneBySHA := map[string][]int{}
	for i, d := range gone {
		if d.SHA256 != "" {
			goneBySHA[d.SHA256+"|"+d.Kind] = append(goneBySHA[d.SHA256+"|"+d.Kind], i)
		}
	}
	movedAway := map[int]bool{}
	var stillNew []change
	for _, c := range newFiles {
		key := c.sha + "|" + c.file.Kind
		if idx := goneBySHA[key]; len(idx) > 0 {
			i := idx[0]
			goneBySHA[key] = idx[1:]
			movedAway[i] = true
			moves = append(moves, change{doc: gone[i], file: c.file, sha: c.sha})
			continue
		}
		stillNew = append(stillNew, c)
	}

	// Pairs are recomputed from the final set of paths.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	pairs, warnings := FindPairs(root, paths, cfg.Pairs)
	rep.Warnings = append(rep.Warnings, warnings...)

	err = store.With(ctx, dbPath, func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			var err error
			if rep.Recovered, err = store.RecoverInterrupted(ctx, tx); err != nil {
				return err
			}
			for i, d := range gone {
				if movedAway[i] {
					continue
				}
				if err := store.DeleteDocument(ctx, tx, d.ID); err != nil {
					return err
				}
				rep.Deleted = append(rep.Deleted, d.Path)
			}
			for _, c := range moves {
				if err := store.Move(ctx, tx, c.doc.ID, c.file.Path, c.file.Size, c.file.MTime); err != nil {
					return err
				}
				rep.Moved = append(rep.Moved, Move{From: c.doc.Path, To: c.file.Path})
			}
			for _, c := range touched {
				if err := store.Touch(ctx, tx, c.doc.ID, c.file.Size, c.file.MTime); err != nil {
					return err
				}
				rep.Unchanged++
			}
			for _, c := range changed {
				if err := store.MarkChanged(ctx, tx, c.doc.ID, c.sha, c.file.Size, c.file.MTime); err != nil {
					return err
				}
				rep.Changed = append(rep.Changed, c.file.Path)
			}
			for _, c := range revived {
				// Back in scope: keep its embeddings if it was fully indexed.
				status := store.StatusPending
				if c.doc.ChunkCount > 0 && c.doc.EmbedID != "" {
					status = store.StatusDone
				}
				if err := store.SetStatus(ctx, tx, c.doc.ID, status); err != nil {
					return err
				}
				if err := store.Touch(ctx, tx, c.doc.ID, c.file.Size, c.file.MTime); err != nil {
					return err
				}
				rep.Revived = append(rep.Revived, c.file.Path)
			}
			for _, c := range stillNew {
				if _, err := store.InsertDocument(ctx, tx, store.Document{
					Path: c.file.Path, Kind: c.file.Kind, SHA256: c.sha, Size: c.file.Size, MTime: c.file.MTime,
				}); err != nil {
					return err
				}
				rep.New = append(rep.New, c.file.Path)
			}
			if err := applyPairs(ctx, tx, pairs, &rep); err != nil {
				return err
			}
			return nil
		})
	})
	if err != nil {
		return rep, err
	}
	err = store.With(ctx, dbPath, func(db *sql.DB) error {
		rep.Queued, err = store.QueuedJobs(ctx, db)
		return err
	})
	if rep.Changes() {
		store.Checkpoint(ctx, dbPath)
	}
	return rep, err
}

// applyPairs rewrites pair_document_id for every document to match pairs.
func applyPairs(ctx context.Context, tx *sql.Tx, pairs []Pair, rep *Report) error {
	docs, err := store.ListDocuments(ctx, tx, "", "")
	if err != nil {
		return err
	}
	ids := make(map[string]int64, len(docs))
	for _, d := range docs {
		ids[d.Path] = d.ID
	}
	want := map[int64]int64{}
	for _, p := range pairs {
		md, pdf := ids[p.MD], ids[p.PDF]
		if md == 0 || pdf == 0 {
			continue
		}
		want[md], want[pdf] = pdf, md
		rep.Pairs = append(rep.Pairs, PairInfo{MD: p.MD, PDF: p.PDF, Via: p.Via})
	}
	for _, d := range docs {
		if want[d.ID] != d.PairID {
			if err := store.SetPair(ctx, tx, d.ID, want[d.ID]); err != nil {
				return err
			}
			// A PDF's text is skipped while it has a transcription, so gaining
			// or losing one means indexing it again.
			if d.Kind == KindPDF && (want[d.ID] != 0) != (d.PairID != 0) && d.Status != store.StatusPending {
				if err := store.SetStatus(ctx, tx, d.ID, store.StatusPending); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sortedFiles(m map[string]File) []File {
	out := make([]File, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b File) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		}
		return 0
	})
	return out
}
