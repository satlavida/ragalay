package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/tui"
)

// runFor runs a command, waiting up to d for it; blocking commands (ticks,
// event waits) are abandoned and keep running in the background.
func runFor(cmd tea.Cmd, d time.Duration) []tea.Msg {
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
				out = append(out, runFor(c, d)...)
			}
			return out
		}
		if msg != nil {
			return []tea.Msg{msg}
		}
	case <-time.After(d):
	}
	return nil
}

func driveFor(m tea.Model, d time.Duration, msgs ...tea.Msg) tea.Model {
	queue := append([]tea.Msg(nil), msgs...)
	for steps := 0; len(queue) > 0 && steps < 300; steps++ {
		msg := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		queue = append(queue, runFor(cmd, d)...)
	}
	return m
}

// TestDoubleClickToSearch is plan1 Phase 7's exit criterion on this machine:
// in a fresh folder, the real interface goes from the welcome screen to a
// working search with key presses only (models already set up).
func TestDoubleClickToSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cache, _ := setup.CacheDir()
	if st, _ := setup.LoadState(cache); !st.Ready(config.Default().Embed.Profile) {
		t.Skip("ragalay setup has not completed on this machine")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	os.MkdirAll(filepath.Join(root, "recipes"), 0o755)
	os.WriteFile(filepath.Join(root, "notes", "transformer.md"), []byte(
		"# Transformer notes\n\nThe encoder is a stack of six identical layers, each with multi-head self-attention."), 0o644)
	os.WriteFile(filepath.Join(root, "recipes", "bread.md"), []byte(
		"# Sourdough\n\nMix flour, water, salt and starter. Bake at 250 degrees."), 0o644)
	t.Chdir(root)

	a := &app{stdin: strings.NewReader(""), stdout: os.Stdout, stderr: os.Stderr}
	b := &tuiBackend{a: a}
	defer b.close()
	var m tea.Model = tui.New(b)
	quick := 300 * time.Millisecond
	m = driveFor(m, quick, tea.WindowSizeMsg{Width: 110, Height: 40}, m.Init()())
	if !strings.Contains(m.View(), "Welcome to ragalay") {
		t.Fatalf("welcome:\n%s", m.View())
	}
	m = driveFor(m, quick, tea.KeyMsg{Type: tea.KeyEnter}) // welcome -> folders
	m = driveFor(m, quick, tea.KeyMsg{Type: tea.KeyEnter}) // everything
	if _, err := os.Stat(filepath.Join(root, ".ragalay", "config.toml")); err != nil {
		t.Fatalf("folder not initialised: %v\n%s", err, m.View())
	}
	if !strings.Contains(m.View(), "Which AI model") {
		t.Fatalf("model step:\n%s", m.View())
	}
	m = driveFor(m, quick, tea.KeyMsg{Type: tea.KeyEnter}) // the preselected default

	// Background indexing runs in the real watcher; wait for it.
	deadline := time.Now().Add(4 * time.Minute)
	for {
		s, err := b.Status(context.Background())
		if err == nil && s.Done == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("indexing did not finish: %+v %v", s, err)
		}
		time.Sleep(time.Second)
	}

	for _, r := range "how many layers does the encoder have" {
		m = driveFor(m, quick, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = driveFor(m, 30*time.Second, tea.KeyMsg{Type: tea.KeyEnter})
	v := m.View()
	if !strings.Contains(v, "notes/transformer.md") || !strings.Contains(v, "six identical layers") {
		t.Fatalf("search did not find the notes:\n%s", v)
	}
	first := strings.Index(v, "notes/transformer.md")
	if bread := strings.Index(v, "recipes/bread.md"); bread >= 0 && bread < first {
		t.Fatalf("bread ranked above the transformer notes:\n%s", v)
	}
	t.Logf("final screen:\n%s", v)
}
