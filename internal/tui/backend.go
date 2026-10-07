// Package tui is ragalay's terminal interface: a first-run setup wizard and
// Search / Status / Folders views (plan1 §4.9). It only talks to Backend, so
// it can be tested with fakes; the CLI provides the real implementation.
package tui

import (
	"context"
	"time"

	"github.com/satlavida/ragalay/internal/search"
)

// Backend is everything the interface needs from ragalay.
type Backend interface {
	// FirstRun says what the wizard has to do.
	FirstRun(ctx context.Context) (FirstRun, error)
	// Init creates .ragalay in dir and sets the folders (empty = everything).
	Init(ctx context.Context, dir string, folders []string) error
	// SetupPlan lists what setup would download.
	SetupPlan(ctx context.Context) (Plan, error)
	// RunSetup installs the models, reporting progress.
	RunSetup(ctx context.Context, ev func(SetupEvent)) error

	Status(ctx context.Context) (Status, error)
	Search(ctx context.Context, query string, o search.Options) (search.Response, error)
	// Watch scans and indexes, then keeps doing so as files change, until
	// ctx ends. It returns ErrBusy-like errors at once when another process
	// is indexing.
	Watch(ctx context.Context, ev func(WatchEvent)) error
	// Reembed rebuilds the index after a model change.
	Reembed(ctx context.Context, ev func(WatchEvent)) error
	AddFolder(ctx context.Context, path string) (string, error)
	RemoveFolder(ctx context.Context, path string, keep bool) (string, error)
	// Open shows a file in the system's default app.
	Open(path string) error
	// CheckUpdate returns a newer released version, or "".
	CheckUpdate(ctx context.Context) (string, error)

	// Models lists the models the folder can use (Model view, wizard).
	Models(ctx context.Context) (Models, error)
	// PlanModel says what switching to a model involves.
	PlanModel(ctx context.Context, c ModelChoice) (ModelPlan, error)
	// UseModel saves the folder's model. allowUpload records consent to
	// send documents to a service on another computer (plan2 S10). The
	// rebuild then runs through Reembed.
	UseModel(ctx context.Context, c ModelChoice, allowUpload bool) error
	// TestModel checks that a model answers and says how.
	TestModel(ctx context.Context, c ModelChoice) (string, error)
	// SetUserEnv stores an environment variable for this user (Windows,
	// plan2 S11).
	SetUserEnv(name, value string) error
}

// Models is the Model view's data.
type Models struct {
	Options   []ModelOption
	Service   Service // the service settings (current or defaults)
	CanSetEnv bool    // SetUserEnv works on this OS
	KeyHelp   string  // how to set the API key variable by hand
}

// ModelOption is one model choice.
type ModelOption struct {
	Name, Title, Pitch, License string
	Local, Installed, Active    bool
}

// Service is an OpenAI-compatible embedding service.
type Service struct {
	BaseURL, Model, KeyEnv, ImageInput string
	KeySet                             bool // the key variable is set
}

// ModelChoice is what the user picked.
type ModelChoice struct {
	Profile string
	Service Service // for the "openai" profile
}

// ModelPlan is what a switch involves.
type ModelPlan struct {
	NeedSetup bool   // models to download first
	License   string // shown before setup
	Remote    bool   // documents leave this computer
	Host      string
	Documents int
}

// FirstRun describes the wizard's work.
type FirstRun struct {
	NeedInit   bool     // no .ragalay yet
	Dir        string   // where ragalay will live
	Subfolders []string // top-level folders the user can pick
	NeedSetup  bool     // models not installed on this computer
	License    string   // model license text to accept
}

// Plan is the download summary shown before setup.
type Plan struct {
	Accelerator string
	Items       []PlanItem
	Total       int64
}

// PlanItem is one download.
type PlanItem struct {
	Name string
	Size int64
}

// SetupEvent is setup progress.
type SetupEvent struct {
	Step, Steps int
	Title       string
	Done, Total int64 // bytes, when downloading
	Note        string
}

// Status is the Status view's data.
type Status struct {
	Root       string
	Documents  int
	Done       int
	Pending    int
	Failed     int
	Chunks     int
	Queued     int
	FailedDocs []FailedDoc
	OtherIndex string // another process is indexing (its command), or ""
	Mismatch   string // model change that needs a re-embed, or ""
	Switch     string // model switch in progress ("12 of 40 documents done"), or ""
	SetupReady bool
	Device     string
	Folders    []Folder
	Whole      bool // indexing the whole directory
}

// FailedDoc is a document that could not be indexed.
type FailedDoc struct{ Path, Error string }

// Folder is one scanned (or kept) folder.
type Folder struct {
	Path      string
	Documents int
	Exists    bool
	Kept      bool
}

// WatchEvent reports background scanning and indexing.
type WatchEvent struct {
	Kind    string // "scan", "progress", "indexed", "error", "busy", "idle"
	Text    string
	Done    int
	Total   int
	Current string
	ETA     time.Duration
}
