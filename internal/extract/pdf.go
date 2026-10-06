package extract

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// PDFReader extracts page text with PDFium compiled to WebAssembly (no CGO;
// plan1 §3.1). Starting it takes ~2.5 s, so it is created on first use and
// shared.
type PDFReader struct {
	once sync.Once
	err  error
	mu   sync.Mutex
	pool pdfium.Pool
	inst pdfium.Pdfium
}

func (r *PDFReader) init() error {
	r.once.Do(func() {
		r.pool, r.err = webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
		if r.err != nil {
			r.err = fmt.Errorf("start PDF reader: %w", r.err)
			return
		}
		r.inst, r.err = r.pool.GetInstance(30 * time.Second)
	})
	return r.err
}

// ErrEncrypted means the PDF needs a password.
var ErrEncrypted = errors.New("the PDF is password-protected")

// Pages returns the text of every page (empty for scanned pages).
func (r *PDFReader) Pages(file string) ([]string, error) {
	if err := r.init(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "password") {
			return nil, ErrEncrypted
		}
		return nil, fmt.Errorf("open PDF: %w", err)
	}
	defer r.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, err := r.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, fmt.Errorf("read PDF: %w", err)
	}
	pages := make([]string, pc.PageCount)
	for i := range pages {
		res, err := r.inst.GetPageText(&requests.GetPageText{Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}})
		if err != nil {
			continue // a broken page: still embed its image
		}
		pages[i] = cleanPageText(res.Text)
	}
	return pages, nil
}

// cleanPageText normalises PDF text: unify line endings, turn PDFium's
// end-of-line hyphenation marker (U+0002, as in "pre\x02train") back into a
// hyphen, drop other control characters, and trim.
func cleanPageText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == 2:
			return '-'
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || r == 0xFFFE || r == 0xFFFF:
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// Close stops the PDF reader.
func (r *PDFReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inst != nil {
		r.inst.Close()
		r.inst = nil
	}
	if r.pool != nil {
		r.pool.Close()
		r.pool = nil
	}
}
