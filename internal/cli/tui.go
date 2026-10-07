package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/index"
	"github.com/satlavida/ragalay/internal/open"
	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/search"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/store"
	"github.com/satlavida/ragalay/internal/tui"
	"github.com/satlavida/ragalay/internal/update"
)

// tuiBackend connects the terminal interface to ragalay.
type tuiBackend struct {
	a    *app
	root string // set once the folder is initialised

	searchOnce sync.Once
	searcher   *liveSearcher
}

func (b *tuiBackend) close() {
	if b.searcher != nil {
		b.searcher.Close()
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
	fr := tui.FirstRun{}
	emb := config.Default().Embed
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
	if b.root != "" {
		if cfg, err := config.Load(b.root); err == nil {
			emb = cfg.Embed
		}
	}
	fr.License = licenseText(emb.Profile)
	cache, err := setup.CacheDir()
	if err != nil {
		return fr, err
	}
	st, _ := setup.LoadState(cache)
	fr.NeedSetup = !setupReady(st, emb)
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
	return setup.Options{Profile: cfg.Embed.Profile, MaxSide: cfg.Embed.ImageMaxSide}, cache, err
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
	// The wizard showed the license screen before this.
	st.AcceptLicense(opts.Profile)
	if err := st.Save(cache); err != nil {
		return err
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
	if sw := rep.ModelSwitch; sw != nil {
		s.Switch = fmt.Sprintf("%d of %d documents done", sw.Done, sw.Total)
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
	b.searchOnce.Do(func() { b.searcher = newLiveSearcher(b.root) })
	return b.searcher.Search(ctx, cfg, q, o)
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
		if rep.Queued == 0 && !index.Switching(ctx, b.root) {
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
	if _, err := index.StartRebuild(ctx, b.root, cfg, false); err != nil {
		return err
	}
	_, err = b.a.indexQueueWith(ctx, b.root, cfg, true, func(p index.Progress) {
		if p.Current != "" {
			ev(tui.WatchEvent{Kind: "progress", Done: p.Done + p.Failed, Total: p.Total, Current: p.Current, ETA: p.ETA})
		}
	})
	return err
}

// settings returns the folder's config, or the defaults before init.
func (b *tuiBackend) settings() config.Config {
	if b.root != "" {
		if cfg, err := config.Load(b.root); err == nil {
			return cfg
		}
	}
	return config.Default()
}

func (b *tuiBackend) Models(ctx context.Context) (tui.Models, error) {
	cfg := b.settings()
	out := tui.Models{CanSetEnv: canSetUserEnv}
	for _, o := range modelOptions(cfg.Embed) {
		out.Options = append(out.Options, tui.ModelOption{Name: o.Name, Title: o.Title, Pitch: o.Pitch,
			License: o.License, Local: o.Local, Installed: o.Installed, Active: o.Active})
	}
	svc := cfg.Embed.OpenAI
	if svc == nil {
		svc = config.DefaultOpenAI()
	}
	out.Service = tui.Service{BaseURL: svc.BaseURL, Model: svc.Model, KeyEnv: svc.APIKeyEnv, ImageInput: svc.ImageInput,
		KeySet: svc.APIKeyEnv != "" && os.Getenv(svc.APIKeyEnv) != ""}
	name := svc.APIKeyEnv
	if name == "" {
		name = "MY_API_KEY"
	}
	out.KeyHelp = keyHelp(name)
	return out, nil
}

// choiceEmbed applies a TUI model choice to emb. A service's dimension is
// detected when it is used.
func choiceEmbed(emb config.Embed, c tui.ModelChoice) (config.Embed, error) {
	e, err := emb.UseProfile(c.Profile, 0)
	if err != nil || c.Profile != embed.OpenAI {
		return e, err
	}
	o := *e.OpenAI
	o.BaseURL, o.Model, o.APIKeyEnv, o.ImageInput = c.Service.BaseURL, c.Service.Model, c.Service.KeyEnv, c.Service.ImageInput
	e.OpenAI, e.Dim = &o, 0
	return e, nil
}

func (b *tuiBackend) PlanModel(ctx context.Context, c tui.ModelChoice) (tui.ModelPlan, error) {
	e, err := choiceEmbed(b.settings().Embed, c)
	if err != nil {
		return tui.ModelPlan{}, err
	}
	var p tui.ModelPlan
	if prof, _ := e.Lookup(); prof.Local() {
		p.NeedSetup = !loadSetupState().Ready(prof.Name)
		p.License = licenseText(prof.Name)
	} else {
		p.Remote, p.Host = e.Remote(), e.OpenAI.Host()
	}
	if b.root != "" {
		store.With(ctx, dbPath(b.root), func(db *sql.DB) error {
			return db.QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&p.Documents)
		})
	}
	return p, nil
}

func (b *tuiBackend) UseModel(ctx context.Context, c tui.ModelChoice, allowUpload bool) error {
	cfg := b.settings()
	e, err := choiceEmbed(cfg.Embed, c)
	if err != nil {
		return err
	}
	if e.Profile == embed.OpenAI {
		res, err := probeModel(ctx, e)
		if err != nil {
			return err
		}
		e.Dim = res.Dim
	}
	cfg.Embed = e
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(b.root, cfg); err != nil {
		return err
	}
	if allowUpload && e.Remote() {
		return store.With(ctx, dbPath(b.root), func(db *sql.DB) error {
			return store.Tx(ctx, db, func(tx *sql.Tx) error {
				return store.SetMeta(ctx, tx, consentKey(e.OpenAI.Host()), time.Now().UTC().Format(time.RFC3339))
			})
		})
	}
	return nil
}

func (b *tuiBackend) TestModel(ctx context.Context, c tui.ModelChoice) (string, error) {
	e, err := choiceEmbed(b.settings().Embed, c)
	if err != nil {
		return "", err
	}
	res, err := probeModel(ctx, e)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d dimensions, %d ms per search", res.Dim, res.QueryMS), nil
}

func (b *tuiBackend) SetUserEnv(name, value string) error {
	if err := setUserEnv(name, value); err != nil {
		return err
	}
	return os.Setenv(name, value) // this window too
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
