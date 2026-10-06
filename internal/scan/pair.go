package scan

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/satlavida/ragalay/internal/config"
)

// Pairing links Markdown transcriptions to their PDFs (plan1 G7). Order:
//  1. front matter `source: <path to pdf>` (relative to the Markdown file)
//  2. a configured [[pairs]] mapping (md = "md/", pdf = "pdf/"), matched by
//     the path inside each folder
//  3. the same base name anywhere (report.md ↔ report.pdf); ambiguous names
//     are left unpaired with a warning

// Pair is one link found by FindPairs.
type Pair struct {
	MD, PDF string
	Via     string // "front matter", "pairs config", "same name"
}

// FindPairs returns the pairs among paths (stored, slash-separated) and
// warnings about links that could not be made. root is used to read front
// matter.
func FindPairs(root string, paths []string, pairs []config.Pair) ([]Pair, []string) {
	var mds []string
	pdfs := map[string]bool{}
	byStem := map[string][]string{}
	for _, p := range paths {
		switch strings.ToLower(path.Ext(p)) {
		case ".md", ".markdown":
			mds = append(mds, p)
		case ".pdf":
			pdfs[p] = true
			stem := strings.ToLower(strings.TrimSuffix(path.Base(p), path.Ext(p)))
			byStem[stem] = append(byStem[stem], p)
		}
	}

	var out []Pair
	var warnings []string
	taken := map[string]string{} // pdf -> md
	link := func(md, pdf, via string) bool {
		if other, ok := taken[pdf]; ok {
			warnings = append(warnings, md+": "+pdf+" is already paired with "+other)
			return false
		}
		taken[pdf] = md
		out = append(out, Pair{MD: md, PDF: pdf, Via: via})
		return true
	}

	// Three passes so a more specific rule always wins, whatever the file order.
	done := map[string]bool{} // md decided (paired, or explicitly pointed elsewhere)
	for _, md := range mds {
		if src := frontMatterSource(filepath.Join(root, filepath.FromSlash(md))); src != "" {
			done[md] = true
			pdf := path.Clean(path.Join(path.Dir(md), filepath.ToSlash(src)))
			if pdfs[pdf] {
				link(md, pdf, "front matter")
			} else {
				warnings = append(warnings, md+": front matter source "+src+" is not an indexed PDF")
			}
		}
	}
	for _, md := range mds {
		if done[md] {
			continue
		}
		if pdf, ok := mirrored(md, pairs); ok && pdfs[pdf] {
			done[md] = link(md, pdf, "pairs config")
		}
	}
	for _, md := range mds {
		if done[md] {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(path.Base(md), path.Ext(md)))
		switch cands := byStem[stem]; {
		case len(cands) == 1:
			link(md, cands[0], "same name")
		case len(cands) > 1:
			warnings = append(warnings, md+": several PDFs named "+path.Base(cands[0])+
				"; add `source: <pdf>` front matter to choose one")
		}
	}
	return out, warnings
}

// mirrored maps md through the [[pairs]] config: md/a/b.md -> pdf/a/b.pdf.
func mirrored(md string, pairs []config.Pair) (string, bool) {
	for _, p := range pairs {
		dir := strings.TrimSuffix(p.MD, "/")
		if config.Covers(dir, md) && md != dir {
			rest := strings.TrimPrefix(md, dir+"/")
			rest = strings.TrimSuffix(rest, path.Ext(rest)) + ".pdf"
			return path.Join(strings.TrimSuffix(p.PDF, "/"), rest), true
		}
	}
	return "", false
}

// frontMatterSource returns the `source:` value from YAML front matter at
// the top of a Markdown file, or "".
func frontMatterSource(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	if !sc.Scan() || strings.TrimSpace(strings.TrimPrefix(sc.Text(), string(rune(0xFEFF)))) != "---" {
		return ""
	}
	for i := 0; i < 200 && sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if line == "---" || line == "..." {
			return ""
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "source" {
			v = strings.TrimSpace(v)
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}
