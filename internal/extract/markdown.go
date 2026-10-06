// Package extract turns documents into the units ragalay embeds: text
// chunks, linked images and PDF pages (plan1 §4.4 step 3).
package extract

import (
	"bytes"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"github.com/satlavida/ragalay/internal/chunk"
	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/scan"
)

// ImageRef is an image linked from a Markdown file.
type ImageRef struct {
	Path        string `json:"path"` // stored form, relative to the root
	Alt         string `json:"alt,omitempty"`
	HeadingPath string `json:"heading_path,omitempty"`
	Page        int    `json:"page,omitempty"`
}

// MarkdownDoc is the result of parsing one Markdown file.
type MarkdownDoc struct {
	Sections []chunk.Section
	Images   []ImageRef
	Warnings []string
}

var (
	md         = goldmark.New(goldmark.WithExtensions(extension.GFM))
	pageMarker = regexp.MustCompile(`(?i)<!--\s*page[\s:]*(\d+)\s*-->`)
	htmlImg    = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	htmlAttr   = regexp.MustCompile(`(?is)\b(src|alt)\s*=\s*("([^"]*)"|'([^']*)')`)
)

// Markdown parses src, the content of the Markdown file rel (stored form)
// inside root.
func Markdown(root, rel string, src []byte) MarkdownDoc {
	src = stripFrontMatter(src)
	doc := md.Parser().Parse(text.NewReader(src))
	var out MarkdownDoc
	var headings []string // headings[i] is the current level-(i+1) heading
	page := 0
	var cur strings.Builder
	headingPath := func() string {
		var parts []string
		for _, h := range headings {
			if h != "" {
				parts = append(parts, h)
			}
		}
		return strings.Join(parts, " > ")
	}
	curPath := ""
	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			out.Sections = append(out.Sections, chunk.Section{HeadingPath: curPath, Text: cur.String(), Page: page})
		}
		cur.Reset()
	}
	seen := map[string]bool{}
	addImage := func(dest, alt string) {
		p, warn := resolveImage(root, rel, dest)
		if warn != "" {
			out.Warnings = append(out.Warnings, warn)
		}
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out.Images = append(out.Images, ImageRef{Path: p, Alt: strings.TrimSpace(alt), HeadingPath: headingPath(), Page: page})
	}

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		switch b := n.(type) {
		case *ast.Heading:
			flush()
			title := strings.TrimSpace(inlineText(b, src))
			for len(headings) < b.Level {
				headings = append(headings, "")
			}
			headings = append(headings[:b.Level-1], title)
			curPath = headingPath()
			continue
		case *ast.HTMLBlock:
			raw := blockSource(b, src)
			if m := pageMarker.FindStringSubmatch(raw); m != nil {
				flush()
				page, _ = strconv.Atoi(m[1])
			}
		}
		ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch v := c.(type) {
			case *ast.Image:
				addImage(string(v.Destination), inlineText(v, src))
			case *ast.RawHTML:
				var b strings.Builder
				for i := 0; i < v.Segments.Len(); i++ {
					s := v.Segments.At(i)
					b.Write(s.Value(src))
				}
				for _, img := range htmlImages(b.String()) {
					addImage(img[0], img[1])
				}
			case *ast.HTMLBlock:
				for _, img := range htmlImages(blockSource(v, src)) {
					addImage(img[0], img[1])
				}
			}
			return ast.WalkContinue, nil
		})
		s := blockSource(n, src)
		if fc, ok := n.(*ast.FencedCodeBlock); ok {
			s = "```" + string(fc.Language(src)) + "\n" + s + "\n```"
		}
		if s = cleanText(pageMarker.ReplaceAllString(s, "")); strings.TrimSpace(s) != "" {
			cur.WriteString(s)
			cur.WriteString("\n\n")
		}
	}
	flush()
	return out
}

var (
	mdImage     = regexp.MustCompile(`!\[([^\]]*)\](?:\([^)]*\)|\[[^\]]*\])`)
	refDef      = regexp.MustCompile(`(?m)^ {0,3}\[[^\]]+\]:\s*\S+.*$`)
	blankLines  = regexp.MustCompile(`\n{3,}`)
	htmlAltAttr = regexp.MustCompile(`(?is)\balt\s*=\s*("([^"]*)"|'([^']*)')`)
)

// cleanText removes image syntax from chunk text, keeping alt text: the
// images are embedded on their own, and URLs are noise for search.
func cleanText(s string) string {
	s = mdImage.ReplaceAllString(s, "$1")
	s = htmlImg.ReplaceAllStringFunc(s, func(tag string) string {
		if m := htmlAltAttr.FindStringSubmatch(tag); m != nil {
			return m[2] + m[3]
		}
		return ""
	})
	s = refDef.ReplaceAllString(s, "")
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

// htmlImages returns (src, alt) for each <img> tag.
func htmlImages(html string) [][2]string {
	var out [][2]string
	for _, tag := range htmlImg.FindAllString(html, -1) {
		var src, alt string
		for _, m := range htmlAttr.FindAllStringSubmatch(tag, -1) {
			v := m[3] + m[4]
			if strings.EqualFold(m[1], "src") {
				src = v
			} else {
				alt = v
			}
		}
		if src != "" {
			out = append(out, [2]string{src, alt})
		}
	}
	return out
}

// blockSource returns the source text spanned by a block and its children.
func blockSource(n ast.Node, src []byte) string {
	start, stop := -1, -1
	var visit func(ast.Node)
	visit = func(x ast.Node) {
		if x.Type() == ast.TypeBlock {
			lines := x.Lines()
			for i := 0; i < lines.Len(); i++ {
				s := lines.At(i)
				if start < 0 || s.Start < start {
					start = s.Start
				}
				if s.Stop > stop {
					stop = s.Stop
				}
			}
		}
		if t, ok := x.(*ast.Text); ok {
			if start < 0 || t.Segment.Start < start {
				start = t.Segment.Start
			}
			if t.Segment.Stop > stop {
				stop = t.Segment.Stop
			}
		}
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			visit(c)
		}
	}
	visit(n)
	if start < 0 || stop <= start {
		return ""
	}
	// Extend to the start of the line so list markers and quotes stay.
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	return strings.TrimRight(string(src[start:stop]), " \t\r\n")
}

// inlineText concatenates the text inside an inline container.
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.CodeSpan:
			for cc := v.FirstChild(); cc != nil; cc = cc.NextSibling() {
				if t, ok := cc.(*ast.Text); ok {
					b.Write(t.Segment.Value(src))
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// stripFrontMatter removes a leading YAML front matter block.
func stripFrontMatter(src []byte) []byte {
	s := bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	lines := bytes.SplitAfter(s, []byte("\n"))
	if len(lines) == 0 || strings.TrimSpace(string(lines[0])) != "---" {
		return src
	}
	off := len(lines[0])
	for _, l := range lines[1:] {
		off += len(l)
		if t := strings.TrimSpace(string(l)); t == "---" || t == "..." {
			return s[off:]
		}
	}
	return src // never closed: not front matter
}

// resolveImage maps a Markdown image destination to a stored path (G6):
// relative to the Markdown file (or the root for "/..."), with an assets/
// fallback next to the Markdown file. Remote images are skipped quietly;
// local problems return a warning.
func resolveImage(root, mdRel, dest string) (string, string) {
	dest = strings.Trim(strings.TrimSpace(dest), "<>")
	if dest == "" {
		return "", ""
	}
	if u, err := url.Parse(dest); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		return "", "" // http(s), data:, etc.; one-letter schemes are Windows drives
	}
	if i := strings.IndexAny(dest, "?#"); i >= 0 {
		dest = dest[:i]
	}
	if un, err := url.PathUnescape(dest); err == nil {
		dest = un
	}
	dest = filepath.ToSlash(dest)
	var p string
	if strings.HasPrefix(dest, "/") {
		p = path.Clean(strings.TrimPrefix(dest, "/"))
	} else {
		p = path.Clean(path.Join(path.Dir(mdRel), dest))
	}
	if p == ".." || strings.HasPrefix(p, "../") || filepath.IsAbs(dest) || filepath.VolumeName(dest) != "" {
		return "", mdRel + ": image " + dest + " is outside the ragalay folder"
	}
	exists := func(rel string) bool {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil && fi.Mode().IsRegular()
	}
	if !exists(p) {
		alt := path.Join(path.Dir(mdRel), "assets", path.Base(dest))
		if !exists(alt) {
			return "", mdRel + ": image " + dest + " not found (keep images in the same folder as the Markdown file)"
		}
		p = alt
	}
	if p == config.DirName || strings.HasPrefix(p, config.DirName+"/") {
		return "", ""
	}
	if _, ok := scan.KindOf(p, []string{"image"}); !ok {
		return "", mdRel + ": " + dest + " is not a supported image type"
	}
	return p, ""
}
