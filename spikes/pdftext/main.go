// Phase 0 spike: compare pure-Go PDF text extraction.
// ledongthuc/pdf vs go-pdfium (WebAssembly build, no CGO).
//
// Usage: go run ./spikes/pdftext <pdf>...
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	lpdf "github.com/ledongthuc/pdf"
)

func main() {
	t := time.Now()
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	inst, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		panic(err)
	}
	fmt.Printf("pdfium wasm init: %v\n\n", time.Since(t))

	for _, path := range os.Args[1:] {
		fmt.Println("=====", path)
		ledong(path)
		pdfiumText(inst, path)
		fmt.Println()
	}
}

func ledong(path string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("  ledongthuc: PANIC", r)
		}
	}()
	t := time.Now()
	f, r, err := lpdf.Open(path)
	if err != nil {
		fmt.Println("  ledongthuc: open error:", err)
		return
	}
	defer f.Close()
	total, empty := 0, 0
	var first string
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		txt, err := p.GetPlainText(nil)
		if err != nil {
			fmt.Printf("  ledongthuc: page %d error: %v\n", i, err)
			continue
		}
		if strings.TrimSpace(txt) == "" {
			empty++
		}
		if i == 1 {
			first = txt
		}
		total += len(txt)
	}
	fmt.Printf("  ledongthuc: %d pages, %d chars, %d empty pages, %v\n", r.NumPage(), total, empty, time.Since(t))
	fmt.Printf("    p1: %q\n", clip(first))
}

func pdfiumText(inst pdfium.Pdfium, path string) {
	t := time.Now()
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		fmt.Println("  pdfium: open error:", err)
		return
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		fmt.Println("  pdfium: page count error:", err)
		return
	}
	total, empty := 0, 0
	var first string
	for i := 0; i < pc.PageCount; i++ {
		res, err := inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}})
		if err != nil {
			fmt.Printf("  pdfium: page %d error: %v\n", i+1, err)
			continue
		}
		if strings.TrimSpace(res.Text) == "" {
			empty++
		}
		if i == 0 {
			first = res.Text
		}
		total += len(res.Text)
	}
	fmt.Printf("  pdfium(wasm): %d pages, %d chars, %d empty pages, %v\n", pc.PageCount, total, empty, time.Since(t))
	fmt.Printf("    p1: %q\n", clip(first))
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}
