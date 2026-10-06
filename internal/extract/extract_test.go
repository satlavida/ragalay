package extract

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/scan"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixtureRoot builds a ragalay folder:
//
//	notes/guide.md                (from testdata) with linked images
//	notes/img/architecture.png    linked relatively
//	notes/assets/logo.png         found through the assets/ fallback
//	notes/screens/tui search.png  linked from HTML with an escaped space
//	notes/img/drawing.svg         unsupported image type
//	pdf/text.pdf                  two pages of text
//	pdf/scanned.pdf               one page, no text layer
//	photos/cat.png                standalone image
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	guide, err := os.ReadFile(filepath.Join("testdata", "guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	put("notes/guide.md", guide)
	for _, p := range []string{"notes/img/architecture.png", "notes/assets/logo.png", "notes/screens/tui search.png", "photos/cat.png"} {
		put(p, pngBytes(t))
	}
	put("notes/img/drawing.svg", []byte("<svg/>"))
	put("pdf/text.pdf", pdfBytes([]string{
		"Attention Is All You Need. The Transformer uses multi-head attention.",
		"Results: BLEU 28.4 on English to German translation.",
	}))
	put("pdf/scanned.pdf", pdfBytes([]string{""}))
	return root
}

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 64; i++ {
		img.Set(i%8, i/8, color.RGBA{uint8(i * 4), 100, 200, 255})
	}
	var b strings.Builder
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return []byte(b.String())
}

// pdfBytes writes a minimal PDF with one page per string (an empty string
// makes a page with no text layer, like a scan).
func pdfBytes(pages []string) []byte {
	var objs []string
	n := len(pages)
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs = append(objs, "<< /Type /Catalog /Pages 2 0 R >>")
	objs = append(objs, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, text := range pages {
		content := ""
		if text != "" {
			content = fmt.Sprintf("BT /F1 12 Tf 40 700 Td (%s) Tj ET", text)
		}
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i))
		objs = append(objs, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj %s endobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func golden(t *testing.T, name string, got Result) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/extract -update)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(b) {
		t.Errorf("%s changed; run with -update if intended.\n--- got ---\n%s", name, b)
	}
}

func extractor(root string) *Extractor {
	return New(root, Options{Tokens: 256, Overlap: 32, PageImages: true})
}

func TestGoldenMarkdownWithLinkedImages(t *testing.T) {
	root := fixtureRoot(t)
	e := extractor(root)
	defer e.Close()
	res, err := e.Extract(context.Background(), scan.KindMarkdown, "notes/guide.md", false)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "guide", res)

	// Spot checks that the golden file must keep true.
	if strings.Contains(res.Units[0].Text, "source:") {
		t.Error("front matter leaked into the text")
	}
	var imgs []string
	for _, l := range res.Links {
		imgs = append(imgs, l.Path)
	}
	want := []string{"notes/img/architecture.png", "notes/assets/logo.png", "notes/screens/tui search.png"}
	if strings.Join(imgs, ",") != strings.Join(want, ",") {
		t.Errorf("linked images = %v, want %v", imgs, want)
	}
	warn := strings.Join(res.Warnings, "\n")
	for _, w := range []string{"img/missing.png not found", "outside the ragalay folder", "drawing.svg is not a supported image"} {
		if !strings.Contains(warn, w) {
			t.Errorf("missing warning %q in:\n%s", w, warn)
		}
	}
	if strings.Contains(warn, "example.com") {
		t.Error("remote images must be skipped without a warning")
	}
	if res.Links[1].HeadingPath != "ragalay guide > Searching > From the terminal" || res.Links[1].Page != 2 {
		t.Errorf("image context wrong: %+v", res.Links[1])
	}
	in := res.Units[len(res.Units)-3].Input(root)
	if in.Modality != embed.Image || !strings.HasSuffix(in.Text, "Architecture diagram") {
		t.Errorf("linked image input should carry its caption: %+v", in)
	}
}

func TestGoldenTextPDF(t *testing.T) {
	e := extractor(fixtureRoot(t))
	defer e.Close()
	res, err := e.Extract(context.Background(), scan.KindPDF, "pdf/text.pdf", false)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "text_pdf", res)
	if res.Pages != 2 || len(res.Units) != 4 {
		t.Fatalf("want 2 text + 2 page units, got %d units over %d pages", len(res.Units), res.Pages)
	}

	// Paired with a transcription: page images only.
	paired, err := e.Extract(context.Background(), scan.KindPDF, "pdf/text.pdf", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range paired.Units {
		if u.Modality != embed.PDFPage {
			t.Errorf("paired PDF must not index its text again: %+v", u)
		}
	}
}

func TestGoldenScannedPDF(t *testing.T) {
	e := extractor(fixtureRoot(t))
	defer e.Close()
	res, err := e.Extract(context.Background(), scan.KindPDF, "pdf/scanned.pdf", false)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "scanned_pdf", res)
	if len(res.Units) != 1 || res.Units[0].Modality != embed.PDFPage {
		t.Fatalf("scanned page should be embedded as an image only: %+v", res.Units)
	}

	// Without page images there is nothing to index.
	e.Opts.PageImages = false
	if _, err := e.Extract(context.Background(), scan.KindPDF, "pdf/scanned.pdf", false); err != ErrNoContent {
		t.Fatalf("want ErrNoContent, got %v", err)
	}
}

func TestGoldenStandaloneImage(t *testing.T) {
	e := extractor(fixtureRoot(t))
	defer e.Close()
	res, err := e.Extract(context.Background(), scan.KindImage, "photos/cat.png", false)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "image", res)
}

func TestBrokenPDF(t *testing.T) {
	root := fixtureRoot(t)
	os.WriteFile(filepath.Join(root, "pdf", "broken.pdf"), []byte("not a pdf"), 0o644)
	e := extractor(root)
	defer e.Close()
	if _, err := e.Extract(context.Background(), scan.KindPDF, "pdf/broken.pdf", false); err == nil {
		t.Fatal("a broken PDF must fail")
	}
}
