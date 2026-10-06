package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/scan"
)

// Unit is one chunk to embed and store.
type Unit struct {
	Modality    string `json:"modality"`               // text, image, pdf_page
	Text        string `json:"text,omitempty"`         // stored text; for images the alt text
	HeadingPath string `json:"heading_path,omitempty"` // "Title > Section"
	Page        int    `json:"page,omitempty"`         // 1-based
	Source      string `json:"source,omitempty"`       // file to embed for image/pdf_page (stored form)
	Tokens      int    `json:"tokens,omitempty"`
}

// Input converts the unit to what the indexing model embeds. Text gets its
// heading path as context; linked images are embedded together with their
// caption (combined input, plan1 §3.1).
func (u Unit) Input(root string) embed.Input {
	switch u.Modality {
	case embed.Text:
		t := u.Text
		if u.HeadingPath != "" {
			t = u.HeadingPath + "\n\n" + t
		}
		return embed.Input{Modality: embed.Text, Text: t}
	case embed.PDFPage:
		return embed.Input{Modality: embed.PDFPage, Path: filepath.Join(root, filepath.FromSlash(u.Source)), Page: u.Page - 1}
	}
	caption := u.Text
	if u.HeadingPath != "" && caption != "" {
		caption = u.HeadingPath + ": " + caption
	}
	return embed.Input{Modality: embed.Image, Path: filepath.Join(root, filepath.FromSlash(u.Source)), Text: caption}
}

// Result is everything extracted from one document.
type Result struct {
	Units    []Unit     `json:"units"`
	Links    []ImageRef `json:"links,omitempty"` // images linked from a Markdown file
	Pages    int        `json:"pages,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
}

// Options control extraction.
type Options struct {
	Tokens, Overlap int
	PageImages      bool // embed each PDF page as an image (covers scanned PDFs)
	Tokenizer       chunk.Tokenizer
}

// ErrNoContent means nothing in the document could be indexed.
var ErrNoContent = errors.New("nothing to index (no text, and page images are turned off)")

// Extractor extracts documents of every kind. It is not safe for concurrent
// use; the PDF reader inside is shared.
type Extractor struct {
	Root string
	Opts Options
	pdf  PDFReader
}

// New returns an Extractor for root.
func New(root string, opts Options) *Extractor {
	if opts.Tokenizer == nil {
		opts.Tokenizer = chunk.Estimate{}
	}
	return &Extractor{Root: root, Opts: opts}
}

// Close releases the PDF reader.
func (e *Extractor) Close() { e.pdf.Close() }

// Extract reads the document at rel (stored form). paired says a Markdown
// transcription exists for this PDF, so its text is not indexed twice.
func (e *Extractor) Extract(ctx context.Context, kind, rel string, paired bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	file := filepath.Join(e.Root, filepath.FromSlash(rel))
	switch kind {
	case scan.KindMarkdown:
		src, err := os.ReadFile(file)
		if err != nil {
			return Result{}, err
		}
		md := Markdown(e.Root, rel, src)
		res := Result{Links: md.Images, Warnings: md.Warnings}
		res.Units = e.textUnits(md.Sections)
		for _, img := range md.Images {
			res.Units = append(res.Units, Unit{Modality: embed.Image, Text: img.Alt, HeadingPath: img.HeadingPath,
				Page: img.Page, Source: img.Path})
		}
		if len(res.Units) == 0 {
			return res, errors.New("the Markdown file is empty")
		}
		return res, nil

	case scan.KindPDF:
		pages, err := e.pdf.Pages(file)
		if err != nil {
			return Result{}, err
		}
		res := Result{Pages: len(pages)}
		if !paired {
			var sections []chunk.Section
			for i, t := range pages {
				sections = append(sections, chunk.Section{Text: t, Page: i + 1})
			}
			res.Units = e.textUnits(sections)
		}
		if e.Opts.PageImages {
			for i := range pages {
				res.Units = append(res.Units, Unit{Modality: embed.PDFPage, HeadingPath: "Page " + strconv.Itoa(i+1),
					Page: i + 1, Source: rel})
			}
		}
		if len(res.Units) == 0 {
			if len(pages) == 0 {
				return res, errors.New("the PDF has no pages")
			}
			if paired {
				return res, nil // its transcription carries the text
			}
			return res, ErrNoContent
		}
		return res, nil

	case scan.KindImage:
		return Result{Units: []Unit{{Modality: embed.Image, Source: rel}}}, nil
	}
	return Result{}, fmt.Errorf("unknown document kind %q", kind)
}

func (e *Extractor) textUnits(sections []chunk.Section) []Unit {
	var out []Unit
	for _, c := range chunk.Split(sections, e.Opts.Tokens, e.Opts.Overlap, e.Opts.Tokenizer) {
		out = append(out, Unit{Modality: embed.Text, Text: c.Text, HeadingPath: c.HeadingPath, Page: c.Page, Tokens: c.Tokens})
	}
	return out
}
