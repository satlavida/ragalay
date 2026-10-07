package tui

import (
	"context"
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
