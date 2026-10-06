package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/open"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/search"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/tui"
	"github.com/satlavida/ragalay/internal/update"
)

// tuiBackend connects the terminal interface to ragalay.
type tuiBackend struct {
	a    *app
	root string // set once the folder is initialised

	searchOnce sync.Once
	searcher   *search.Searcher
	closeQuery func()
}

func (b *tuiBackend) close() {
	if b.closeQuery != nil {
		b.closeQuery()
	}
}

// defaultDir is where a double-clicked ragalay sets itself up: the folder
// the binary sits in ("drop it into a folder"), unless it is a `go run`
// build in a temp folder.
func defaultDir() string {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		if !strings.Contains(strings.ToLower(dir), "go-build") {
			return dir
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func (b *tuiBackend) FirstRun(ctx context.Context) (tui.FirstRun, error) {
	fr := tui.FirstRun{License: licenseText}
	if root, err := b.a.root(); err == nil {
		b.root, fr.Dir = root, root
	} else if errors.Is(err, config.ErrNotInitialized) {
		fr.NeedInit, fr.Dir = true, defaultDir()
		if entries, err := os.ReadDir(fr.Dir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && len(fr.Subfolders) < 40 {
					fr.Subfolders = append(fr.Subfolders, e.Name())
				}
			}
		}
	} else {
		return fr, err
	}
	cache, err := setup.CacheDir()
	if err != nil {
		return fr, err
	}
	st, _ := setup.LoadState(cache)
	fr.NeedSetup = !st.Ready()
	return fr, nil
}

func (b *tuiBackend) Init(ctx context.Context, dir string, folders []string) error {
	if _, err := initRoot(ctx, dir); err != nil {
		return err
	}
	b.root = dir
	b.a.rootFlag = dir
	if len(folders) > 0 {
		_, err := addFolders(dir, dir, folders)
		return err
	}
	return nil
}

func (b *tuiBackend) setupOpts() (setup.Options, string, error) {
	cfg, err := config.Load(b.root)
	if err != nil {
		return setup.Options{}, "", err
	}
	cache, err := setup.CacheDir()
	return setup.Options{Model: cfg.Embed.IndexModel, Revision: cfg.Embed.IndexRevision, MaxSide: cfg.Embed.ImageMaxSide}, cache, err
}

func (b *tuiBackend) SetupPlan(ctx context.Context) (tui.Plan, error) {
	opts, cache, err := b.setupOpts()
	if err != nil {
		return tui.Plan{}, err
	}
	p, err := setup.Prepare(ctx, cache, opts)
	if err != nil {
		return tui.Plan{}, err
	}
	out := tui.Plan{Accelerator: p.Detection.Reason, Total: p.Total}
	for _, d := range p.Downloads {
		out.Items = append(out.Items, tui.PlanItem{Name: d.Name, Size: d.Size})
	}
	return out, nil
}

// tuiReporter turns setup progress into wizard events.
type tuiReporter struct {
	ev   func(tui.SetupEvent)
	last tui.SetupEvent
}

func (r *tuiReporter) Step(n, total int, title string) {
	r.last = tui.SetupEvent{Step: n, Steps: total, Title: title}
	r.ev(r.last)
}
func (r *tuiReporter) Progress(done, total int64) {
	e := r.last
	e.Done, e.Total = done, total
	r.ev(e)
}
func (r *tuiReporter) Note(msg string) {
	e := r.last
	e.Note = msg
	r.ev(e)
}

func (b *tuiBackend) RunSetup(ctx context.Context, ev func(tui.SetupEvent)) error {
	opts, cache, err := b.setupOpts()
	if err != nil {
		return err
	}
	st, err := setup.LoadState(cache)
	if err != nil {
		return err
	}
	if st.LicenseAccepted == "" {
		st.LicenseAccepted = time.Now().UTC().Format(time.RFC3339)
		if err := st.Save(cache); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(filepath.Join(b.root, config.DirName, config.LogsDir, "setup.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	opts.Log, opts.Report = logFile, &tuiReporter{ev: ev}
	st, err = setup.Run(ctx, cache, opts)
	if err != nil {
		return err
	}
	cfg, err := config.Load(b.root)
	if err != nil {
		return err
	}
	return recordSetup(ctx, b.root, cfg, st)
}

func (b *tuiBackend) Status(ctx context.Context) (tui.Status, error) {
	rep, err := statusReport(ctx, b.root)
	if err != nil {
		return tui.Status{}, err
	}
	s := tui.Status{Root: rep.Root, Documents: rep.Index.Documents, Done: rep.Index.ByStatus["done"],
		Pending: rep.Index.ByStatus["pending"] + rep.Index.ByStatus["processing"], Failed: rep.Index.ByStatus["failed"],
		Chunks: rep.Index.Chunks, Queued: rep.Queued, SetupReady: rep.Setup.Ready, Device: rep.Setup.DeviceName,
		Whole: rep.WholeDir}
	if rep.Indexing != nil && rep.Indexing.PID != os.Getpid() {
		s.OtherIndex = rep.Indexing.Command
	}
	if m := rep.ModelMismatch; m != nil {
		s.Mismatch = m.Index + " -> " + m.Config
	}
	for _, f := range rep.Folders {
		s.Folders = append(s.Folders, tui.Folder{Path: f.Path, Documents: f.Documents, Exists: f.Exists})
	}
	if cfg, err := config.Load(b.root); err == nil {
		for _, k := range cfg.Scan.Keep {
			s.Folders = append(s.Folders, tui.Folder{Path: k, Exists: true, Kept: true})
		}
	}
	if s.Failed > 0 {
		if docs, err := docInfos(ctx, b.root, "failed", ""); err == nil {
			for _, d := range docs {
				s.FailedDocs = append(s.FailedDocs, tui.FailedDoc{Path: d.Path, Error: d.Error})
			}
		}
	}
	return s, nil
}

func (b *tuiBackend) Search(ctx context.Context, q string, o search.Options) (search.Response, error) {
	cfg, err := config.Load(b.root)
	if err != nil {
		return search.Response{}, err
	}
	b.searchOnce.Do(func() { b.searcher, b.closeQuery, _ = newSearcher(b.root, cfg) })
	b.searcher.Cfg = cfg
	return b.searcher.Search(ctx, q, o)
}

// Watch scans and indexes in the background while the interface is open.
func (b *tuiBackend) Watch(ctx context.Context, ev func(tui.WatchEvent)) error {
	cfg, err := config.Load(b.root)
	if err != nil {
		return err
	}
	l, err := lockAcquire(b.root, "ragalay (window)")
	if err != nil {
		return err
	}
	defer l.Release()
	progress := func(p index.Progress) {
		if p.Current == "" {
			return
		}
		ev(tui.WatchEvent{Kind: "progress", Done: p.Done + p.Failed, Total: p.Total, Current: p.Current, ETA: p.ETA})
	}
	return scan.Watch(ctx, b.root, 2*time.Second, func(rep scan.Report, err error) {
		if err != nil {
			ev(tui.WatchEvent{Kind: "error", Text: err.Error()})
			return
		}
		ev(tui.WatchEvent{Kind: "scan", Text: strconv.Itoa(rep.Found) + " files"})
		if err := index.CheckSpace(ctx, b.root, cfg); err != nil {
			ev(tui.WatchEvent{Kind: "error", Text: "model changed: press R to rebuild"})
			return
		}
		if rep.Queued == 0 {
			ev(tui.WatchEvent{Kind: "idle"})
			return
		}
		sum, err := b.a.indexQueueWith(ctx, b.root, cfg, true, progress)
		switch {
		case errors.Is(err, errNotSetUp), errors.Is(err, context.Canceled):
			ev(tui.WatchEvent{Kind: "idle"})
		case err != nil:
			ev(tui.WatchEvent{Kind: "error", Text: err.Error()})
		default:
			ev(tui.WatchEvent{Kind: "indexed", Text: summaryText(sum)})
		}
	})
}

func (b *tuiBackend) Reembed(ctx context.Context, ev func(tui.WatchEvent)) error {
	cfg, err := config.Load(b.root)
	if err != nil {
		return err
	}
	l, err := lockAcquire(b.root, "reembed")
	if err != nil {
		return err
	}
	defer l.Release()
	if _, err := scan.Run(ctx, b.root, cfg); err != nil {
		return err
	}
	if _, err := index.Reembed(ctx, b.root, cfg); err != nil {
		return err
	}
	_, err = b.a.indexQueueWith(ctx, b.root, cfg, true, func(p index.Progress) {
		if p.Current != "" {
			ev(tui.WatchEvent{Kind: "progress", Done: p.Done + p.Failed, Total: p.Total, Current: p.Current, ETA: p.ETA})
		}
	})
	return err
}

func (b *tuiBackend) AddFolder(ctx context.Context, p string) (string, error) {
	notes, err := addFolders(b.root, b.root, []string{p})
	return strings.Join(notes, "; "), err
}

func (b *tuiBackend) RemoveFolder(ctx context.Context, p string, keep bool) (string, error) {
	notes, err := removeFolders(ctx, b.root, b.root, []string{p}, keep)
	return strings.Join(notes, "; "), err
}

func (b *tuiBackend) Open(p string) error {
	return open.File(filepath.Join(b.root, filepath.FromSlash(p)))
}

func (b *tuiBackend) CheckUpdate(ctx context.Context) (string, error) {
	cfg, err := config.Load(b.root)
	if err != nil || !cfg.Update.Check {
		return "", err
	}
	cache, err := setup.CacheDir()
	if err != nil {
		return "", err
	}
	return update.Check(ctx, Version, cache)
}

func summaryText(s *index.Summary) string {
	if s == nil {
		return ""
	}
	return pluralDocs(s.Indexed+s.Linked) + " indexed"
}

func pluralDocs(n int) string {
	if n == 1 {
		return "1 document"
	}
	return strconv.Itoa(n) + " documents"
}
