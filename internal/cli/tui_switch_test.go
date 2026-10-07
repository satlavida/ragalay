package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/search"
	"github.com/satlavida/ragalay/internal/setup"
	"github.com/satlavida/ragalay/internal/tui"
)

// TestTUIBackendSwitchesModels drives the window's real backend through a
// model switch with the real models: what the Model view does after the
// user confirms (plan2 Phase 5). The screens themselves are tested with a
// fake backend in internal/tui.
func TestTUIBackendSwitchesModels(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cache, _ := setup.CacheDir()
	st, _ := setup.LoadState(cache)
	if !st.Ready(embed.Gemma2) || !st.Ready(embed.JinaV5) {
		t.Skip("needs both local models set up on this machine")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.md"), []byte("# Notes\n\nThe encoder is a stack of six identical layers."), 0o644)
	ctx := context.Background()
	a := &app{stdin: strings.NewReader(""), stdout: os.Stdout, stderr: os.Stderr}
	b := &tuiBackend{a: a}
	defer b.close()
	if err := b.Init(ctx, root, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.UseModel(ctx, tui.ModelChoice{Profile: embed.JinaV5}, false); err != nil {
		t.Fatal(err)
	}
	// First indexing, as the window's background watch does it.
	if err := b.Reembed(ctx, func(tui.WatchEvent) {}); err != nil {
		t.Fatal(err)
	}
	ms, _ := b.Models(ctx)
	if !ms.Options[1].Active || ms.Options[1].Name != embed.JinaV5 {
		t.Fatalf("models %+v", ms.Options)
	}

	choice := tui.ModelChoice{Profile: embed.Gemma2}
	plan, err := b.PlanModel(ctx, choice)
	if err != nil || plan.NeedSetup || plan.Remote || plan.Documents != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if err := b.UseModel(ctx, choice, false); err != nil {
		t.Fatal(err)
	}
	s, _ := b.Status(ctx)
	if s.Mismatch == "" {
		t.Fatal("the window should offer the switch (R)")
	}
	// Search keeps working on Jina until the switch runs.
	resp, err := b.Search(ctx, "how many layers", searchOpts())
	if err != nil || len(resp.Results) == 0 || resp.Mode != "hybrid" {
		t.Fatalf("search before the switch: %+v %v", resp, err)
	}
	t0 := time.Now()
	if err := b.Reembed(ctx, func(tui.WatchEvent) {}); err != nil {
		t.Fatal(err)
	}
	rep, err := statusReport(ctx, root)
	if err != nil || !strings.HasPrefix(rep.Index.EmbedID, "google/embeddinggemma-2") || rep.ModelSwitch != nil || rep.ModelMismatch != nil {
		t.Fatalf("after the switch: %+v %v", rep, err)
	}
	t.Logf("switch took %v", time.Since(t0))
	resp, err = b.Search(ctx, "how many layers", searchOpts())
	if err != nil || len(resp.Results) == 0 || resp.Notice != "" {
		t.Fatalf("search after the switch: %+v %v", resp, err)
	}
}

func searchOpts() search.Options { return search.Options{K: 5} }
