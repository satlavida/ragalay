package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/satlavida/ragalay/internal/search"
)

type fakeBackend struct {
	mu        sync.Mutex
	first     FirstRun
	inited    []string
	initDir   string
	setupRan  bool
	watches   int
	reembeds  int
	opened    []string
	added     []string
	removed   []string
	mismatch  string
	setupDone bool
	docs      int
	profile   string
	used      []ModelChoice
	allowed   bool
	env       string
}

func (f *fakeBackend) FirstRun(context.Context) (FirstRun, error) { return f.first, nil }
func (f *fakeBackend) Init(_ context.Context, dir string, folders []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initDir, f.inited = dir, folders
	return nil
}
func (f *fakeBackend) SetupPlan(context.Context) (Plan, error) {
	return Plan{Accelerator: "AMD Radeon RX 9070 XT", Items: []PlanItem{{"indexing model", 4 << 30}}, Total: 4 << 30}, nil
}
func (f *fakeBackend) RunSetup(_ context.Context, ev func(SetupEvent)) error {
	ev(SetupEvent{Step: 1, Steps: 7, Title: "Python manager", Done: 5 << 20, Total: 17 << 20})
	ev(SetupEvent{Step: 7, Steps: 7, Title: "Self-test", Note: "Search and indexing models agree: 0.9997"})
	f.mu.Lock()
	f.setupRan, f.setupDone = true, true
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) Status(context.Context) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{Root: "/docs", Documents: 3, Done: 3, Chunks: 40, SetupReady: f.setupDone || !f.first.NeedSetup,
		Device: "AMD Radeon RX 9070 XT", Mismatch: f.mismatch,
		Folders: []Folder{{Path: "notes", Documents: 2, Exists: true}, {Path: "papers", Documents: 1, Exists: true}}}, nil
}
func (f *fakeBackend) Search(_ context.Context, q string, _ search.Options) (search.Response, error) {
	return search.Response{Query: q, Mode: "hybrid", Results: []search.Result{
		{Score: 0.03, Path: "papers/attention.pdf", Kind: "pdf", Modality: "text", Page: 3,
			Text: "The encoder is composed of a stack of N = 6 identical layers."},
		{Score: 0.02, Path: "notes/img/heads.png", Kind: "image", Modality: "image", ParentPath: "notes/guide.md"},
	}}, nil
}
func (f *fakeBackend) Watch(ctx context.Context, ev func(WatchEvent)) error {
	f.mu.Lock()
	f.watches++
	f.mu.Unlock()
	ev(WatchEvent{Kind: "progress", Done: 0, Total: 3, Current: "papers/attention.pdf", ETA: 40 * time.Second})
	ev(WatchEvent{Kind: "indexed", Text: "3 documents"})
	<-ctx.Done()
	return nil
}
func (f *fakeBackend) Reembed(context.Context, func(WatchEvent)) error {
	f.mu.Lock()
	f.reembeds++
	f.mismatch = ""
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) AddFolder(_ context.Context, p string) (string, error) {
	f.added = append(f.added, p)
	return "added " + p, nil
}
func (f *fakeBackend) RemoveFolder(_ context.Context, p string, keep bool) (string, error) {
	f.removed = append(f.removed, p)
	return "removed " + p, nil
}
func (f *fakeBackend) Open(p string) error {
	f.mu.Lock()
	f.opened = append(f.opened, p)
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) CheckUpdate(context.Context) (string, error) { return "v1.2.0", nil }

func (f *fakeBackend) Models(context.Context) (Models, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := f.profile
	if active == "" {
		active = "embeddinggemma-2"
	}
	opts := []ModelOption{
		{Name: "embeddinggemma-2", Title: "EmbeddingGemma 2", Pitch: "recommended", Local: true, Installed: true},
		{Name: "jina-v5", Title: "Jina v5", Pitch: "non-commercial", Local: true},
		{Name: "openai", Title: "Another service (OpenAI-compatible)", Pitch: "Ollama, ..."},
	}
	for i := range opts {
		opts[i].Active = opts[i].Name == active
	}
	return Models{Options: opts, Service: Service{BaseURL: "http://localhost:11434/v1", Model: "nomic-embed-text", ImageInput: "none"},
		CanSetEnv: true, KeyHelp: "setx NAME value"}, nil
}
func (f *fakeBackend) PlanModel(_ context.Context, c ModelChoice) (ModelPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := ModelPlan{Documents: f.docs}
	if c.Profile == "jina-v5" {
		p.NeedSetup, p.License = true, "CC BY-NC 4.0: personal and research use"
	}
	if c.Profile == "embeddinggemma-2" && f.first.NeedSetup && !f.setupDone {
		p.NeedSetup, p.License = true, f.first.License
	}
	if c.Profile == "openai" && !strings.Contains(c.Service.BaseURL, "localhost") {
		p.Remote, p.Host = true, "api.example.com"
	}
	return p, nil
}
func (f *fakeBackend) UseModel(_ context.Context, c ModelChoice, allow bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used, f.allowed, f.profile = append(f.used, c), allow, c.Profile
	if f.docs > 0 {
		f.mismatch = "old -> " + c.Profile
	}
	return nil
}
func (f *fakeBackend) TestModel(_ context.Context, c ModelChoice) (string, error) {
	if c.Service.Model == "broken" {
		return "", errors.New("nothing answers")
	}
	return "768 dimensions", nil
}
func (f *fakeBackend) SetUserEnv(name, value string) error {
	f.mu.Lock()
	f.env = name + "=" + value
	f.mu.Unlock()
	return nil
}

// drive feeds messages to the model and runs the commands they return,
// skipping commands that block (ticks, waits on channels).
func drive(m tea.Model, msgs ...tea.Msg) tea.Model {
	queue := append([]tea.Msg(nil), msgs...)
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		queue = append(queue, run(cmd)...)
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, run(c)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func view(m tea.Model) string { return m.View() }

// TestFirstRunToSearch is plan1 Phase 7's exit criterion, minus the real
// double-click: a fresh folder on a fresh computer reaches working search
// with key presses only.
func TestFirstRunToSearch(t *testing.T) {
	f := &fakeBackend{first: FirstRun{NeedInit: true, Dir: "/home/me/Documents", Subfolders: []string{"notes", "papers"},
		NeedSetup: true, License: "CC BY-NC 4.0: personal and research use"}}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 40}, m.Init()())
	if !strings.Contains(view(m), "Welcome to ragalay") || !strings.Contains(view(m), "/home/me/Documents") {
		t.Fatalf("welcome screen:\n%s", view(m))
	}
	m = drive(m, key("enter"))
	if !strings.Contains(view(m), "Which folders") {
		t.Fatalf("folder picker:\n%s", view(m))
	}
	m = drive(m, key("down"), key("space"), key("enter")) // just "notes"
	if f.initDir != "/home/me/Documents" || len(f.inited) != 1 || f.inited[0] != "notes" {
		t.Fatalf("init with %q %v", f.initDir, f.inited)
	}
	// The model step: EmbeddingGemma 2 is preselected (plan2 S18).
	if v := view(m); !strings.Contains(v, "Which AI model") || !strings.Contains(v, "> EmbeddingGemma 2") {
		t.Fatalf("model step:\n%s", v)
	}
	m = drive(m, key("enter"))
	if len(f.used) != 1 || f.used[0].Profile != "embeddinggemma-2" {
		t.Fatalf("model chosen: %+v", f.used)
	}
	if !strings.Contains(view(m), "CC BY-NC") {
		t.Fatalf("license screen:\n%s", view(m))
	}
	m = drive(m, key("y"))
	if !strings.Contains(view(m), "RX 9070 XT") || !strings.Contains(view(m), "4.0 GB") {
		t.Fatalf("download plan:\n%s", view(m))
	}
	m = drive(m, key("enter"))
	if !f.setupRan {
		t.Fatal("setup did not run")
	}
	v := view(m)
	if !strings.Contains(v, "[Search]") {
		t.Fatalf("main screen after setup:\n%s", v)
	}
	if f.watches != 1 {
		t.Fatalf("background indexing should start once, started %d", f.watches)
	}
	if !strings.Contains(v, "v1.2.0") {
		t.Errorf("update notice missing:\n%s", v)
	}

	m = typeText(m, "how many encoder layers")
	m = drive(m, key("enter"))
	v = view(m)
	if !strings.Contains(v, "papers/attention.pdf · p.3") || !strings.Contains(v, "N = 6 identical layers") {
		t.Fatalf("search results:\n%s", v)
	}
	// Open the PDF, then the image's document and the image itself.
	m = drive(m, key("esc"), key("enter"))
	m = drive(m, key("down"), key("enter"), key("o"))
	if strings.Join(f.opened, ",") != "papers/attention.pdf,notes/guide.md,notes/img/heads.png" {
		t.Fatalf("opened %v", f.opened)
	}
}

func TestSetUpFolderGoesStraightToSearch(t *testing.T) {
	f := &fakeBackend{first: FirstRun{Dir: "/x"}}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 90, Height: 30}, m.Init()())
	if !strings.Contains(view(m), "[Search]") {
		t.Fatalf("expected the main screen:\n%s", view(m))
	}
	// Folders view: add and remove.
	m = drive(m, key("tab"), key("tab"))
	if !strings.Contains(view(m), "[Folders]") || !strings.Contains(view(m), "papers") {
		t.Fatalf("folders view:\n%s", view(m))
	}
	m = drive(m, key("a"))
	m = typeText(m, "photos")
	m = drive(m, key("enter"), key("down"), key("d"))
	if len(f.added) != 1 || f.added[0] != "photos" || len(f.removed) != 1 || f.removed[0] != "papers" {
		t.Fatalf("added %v removed %v", f.added, f.removed)
	}
}

func TestMismatchRebuild(t *testing.T) {
	f := &fakeBackend{first: FirstRun{Dir: "/x"}, mismatch: "model@a:1024 -> model@a:512"}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 90, Height: 30}, m.Init()())
	if !strings.Contains(view(m), "Press R to switch") {
		t.Fatalf("mismatch banner missing:\n%s", view(m))
	}
	m = drive(m, key("esc")) // leave the search box so R is a command
	mm := m.(Model)
	mm.inputOn = false
	m = drive(mm, key("R"))
	// The fake watch only stops when its context is cancelled; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for f.reembeds == 0 && time.Now().Before(deadline) {
		m = drive(m, watchDoneMsg{gen: m.(Model).watchGen})
	}
	if f.reembeds != 1 {
		t.Fatalf("reembed ran %d times", f.reembeds)
	}
}

// waitReembed delivers the stopped-watch message the fake watch only sends
// when its context ends, until the rebuild ran.
func waitReembed(m tea.Model, f *fakeBackend) tea.Model {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := f.reembeds
		f.mu.Unlock()
		if n > 0 {
			break
		}
		m = drive(m, watchDoneMsg{gen: m.(Model).watchGen})
	}
	return m
}

func tabTo(m tea.Model, name string) tea.Model {
	for i := 0; i < 5 && !strings.Contains(view(m), "["+name+"]"); i++ {
		m = drive(m, key("tab"))
	}
	return m
}

// Switching to a service on another computer asks first, records consent
// and rebuilds (plan2 S10, S1).
func TestModelViewSwitchesToService(t *testing.T) {
	f := &fakeBackend{first: FirstRun{Dir: "/x"}, docs: 12}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 110, Height: 40}, m.Init()())
	m = drive(m, key("esc"))
	m = tabTo(m, "Model")
	if v := view(m); !strings.Contains(v, "> EmbeddingGemma 2") || !strings.Contains(v, "(this folder)") {
		t.Fatalf("model view:\n%s", v)
	}
	m = drive(m, key("down"), key("down"), key("enter"))
	if !strings.Contains(view(m), "Address:") {
		t.Fatalf("service form:\n%s", view(m))
	}
	// Replace the address with an online one.
	for i := 0; i < 30; i++ {
		m = drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = typeText(m, "https://api.example.com/v1")
	m = drive(m, key("tab"), key("tab"))
	m = typeText(m, "MY_KEY")
	m = drive(m, tea.KeyMsg{Type: tea.KeyCtrlK})
	m = typeText(m, "sk-123")
	m = drive(m, key("enter"))
	if f.env != "MY_KEY=sk-123" {
		t.Fatalf("key not stored: %q", f.env)
	}
	m = drive(m, key("enter"))
	v := view(m)
	if !strings.Contains(v, "sent to api.example.com") || !strings.Contains(v, "12 documents") {
		t.Fatalf("consent screen:\n%s", v)
	}
	m = waitReembed(drive(m, key("y")), f)
	if len(f.used) != 1 || !f.allowed || f.used[0].Service.BaseURL != "https://api.example.com/v1" || f.used[0].Service.KeyEnv != "MY_KEY" {
		t.Fatalf("used %+v allowed %v", f.used, f.allowed)
	}
	if f.reembeds != 1 {
		t.Fatalf("the switch should rebuild once, rebuilt %d times", f.reembeds)
	}
}

// A model that is not installed is downloaded first, then the switch runs.
func TestModelViewInstallsThenSwitches(t *testing.T) {
	f := &fakeBackend{first: FirstRun{Dir: "/x"}, docs: 3, setupDone: true}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 110, Height: 40}, m.Init()())
	m = drive(m, key("esc"))
	m = tabTo(m, "Model")
	m = drive(m, key("down"), key("enter"))
	if !strings.Contains(view(m), "downloaded first") {
		t.Fatalf("confirm:\n%s", view(m))
	}
	m = drive(m, key("y"))
	if !strings.Contains(view(m), "CC BY-NC") {
		t.Fatalf("license:\n%s", view(m))
	}
	f.setupRan = false
	m = drive(m, key("y"))
	m = waitReembed(drive(m, key("enter")), f)
	if !f.setupRan || f.reembeds != 1 || !strings.Contains(view(m), "Index rebuilt") || !strings.Contains(view(m), "Jina v5                                non-commercial  (this folder)") {
		t.Fatalf("setup %v reembeds %d\n%s", f.setupRan, f.reembeds, view(m))
	}
}

// A service that does not answer is reported and nothing changes.
func TestModelViewBrokenService(t *testing.T) {
	f := &fakeBackend{first: FirstRun{Dir: "/x"}}
	var m tea.Model = New(f)
	m = drive(m, tea.WindowSizeMsg{Width: 110, Height: 40}, m.Init()())
	m = drive(m, key("esc"))
	m = tabTo(m, "Model")
	m = drive(m, key("down"), key("down"), key("enter"), key("tab"))
	for i := 0; i < 20; i++ {
		m = drive(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = typeText(m, "broken")
	m = drive(m, key("enter"))
	if !strings.Contains(view(m), "did not answer") || len(f.used) != 0 {
		t.Fatalf("broken service:\n%s", view(m))
	}
}
