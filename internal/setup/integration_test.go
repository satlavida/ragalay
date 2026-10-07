package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/sidecar"
)

// minimalPDF is a one-page PDF with a line of text, enough for pdfium.
const minimalPDF = `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj
4 0 obj << /Length 58 >> stream
BT /F1 18 Tf 30 100 Td (Multi-head attention) Tj ET
endstream endobj
5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
trailer << /Root 1 0 R >>
%%EOF
`

// TestBothRuntimesOnThisMachine embeds through the real Python sidecar and
// llama.cpp for every local profile set up on this machine (plan1 Phase 2,
// plan2 Phase 2). Profiles that are not set up are skipped.
func TestBothRuntimesOnThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, prof := range embed.Profiles() {
		if !prof.Local() {
			continue
		}
		t.Run(prof.Name, func(t *testing.T) {
			if !st.Ready(prof.Name) {
				t.Skipf("ragalay setup has not completed for %s on this machine", prof.Name)
			}
			bothRuntimes(t, st, prof)
		})
	}
}

func bothRuntimes(t *testing.T, st State, prof embed.Profile) {
	cfg := config.Default()
	dim := prof.DefaultDim
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	pdf := filepath.Join(dir, "doc.pdf")
	os.WriteFile(pdf, []byte(minimalPDF), 0o644)
	img, err := testImage()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(img)

	sc, err := sidecar.Start(ctx, sidecar.Options{
		Python: st.Python, ScriptPath: st.Sidecar, Model: prof.IndexModel,
		Revision: prof.IndexRevision, MaxSide: cfg.Embed.ImageMaxSide,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	t.Logf("sidecar: %+v", sc.Info())

	docs, err := sc.EmbedDocuments(ctx, []embed.Input{
		{Modality: embed.Text, Text: "Multi-head attention lets the model attend to several positions at once.", Title: "notes.md"},
		{Modality: embed.Text, Text: "Sourdough bread needs a starter, flour, water and salt.", Title: "notes.md"},
		{Modality: embed.Image, Path: img},
		{Modality: embed.Image, Path: img, Text: "a colourful test pattern"},
		{Modality: embed.PDFPage, Path: pdf, Page: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range docs {
		if len(d) != dim {
			t.Fatalf("doc %d has %d dims, want %d", i, len(d), dim)
		}
	}

	lq, err := llama.Open(st.LlamaLib, st.Profile(prof.Name).QueryModel, llama.OptionsFor(prof))
	if err != nil {
		t.Fatal(err)
	}
	defer lq.Close()
	raw, err := lq.EmbedQuery(ctx, "how does multi-head attention work")
	if err != nil {
		t.Fatal(err)
	}
	q, err := embed.Fit(raw, dim)
	if err != nil {
		t.Fatal(err)
	}

	attention, bread, page := embed.Cosine(q, docs[0]), embed.Cosine(q, docs[1]), embed.Cosine(q, docs[4])
	t.Logf("query vs attention text %.3f, bread text %.3f, PDF page %.3f", attention, bread, page)
	if attention <= bread {
		t.Errorf("llama.cpp query should match the attention text (%.3f) over bread (%.3f)", attention, bread)
	}
	if page <= bread {
		t.Errorf("the PDF page about attention (%.3f) should beat the bread text (%.3f)", page, bread)
	}
	c := embed.Cosine(docs[2], docs[3])
	t.Logf("image with and without caption %.3f", c)
	if c < 0.8 {
		t.Errorf("image with and without caption should be close, got %.3f", c)
	}

	// Matryoshka: a 256-dim index still ranks the same way.
	q256, _ := embed.Fit(append([]float32(nil), raw...), 256)
	a256, _ := embed.Fit(append([]float32(nil), docs[0]...), 256)
	b256, _ := embed.Fit(append([]float32(nil), docs[1]...), 256)
	if embed.Cosine(q256, a256) <= embed.Cosine(q256, b256) {
		t.Error("256-dim truncation changed the ranking")
	}
}
