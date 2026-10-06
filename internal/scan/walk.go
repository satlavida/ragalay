// Package scan finds the documents in the configured folders and keeps the
// documents table in step with the files on disk (plan1 §4.4 step 1).
package scan

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/satlavida/ragalay/internal/config"
)

// Document kinds stored in the documents table.
const (
	KindMarkdown = "markdown"
	KindPDF      = "pdf"
	KindImage    = "image"
)

// extensions maps file extensions to (config kind, document kind).
var extensions = map[string][2]string{
	".md":       {"md", KindMarkdown},
	".markdown": {"md", KindMarkdown},
	".pdf":      {"pdf", KindPDF},
	".png":      {"image", KindImage},
	".jpg":      {"image", KindImage},
	".jpeg":     {"image", KindImage},
	".webp":     {"image", KindImage},
	".gif":      {"image", KindImage},
	".bmp":      {"image", KindImage},
}

// KindOf returns the document kind for a path if its extension is one of
// the enabled config kinds.
func KindOf(p string, enabled []string) (string, bool) {
	k, ok := extensions[strings.ToLower(path.Ext(p))]
	if !ok || !slices.Contains(enabled, k[0]) {
		return "", false
	}
	return k[1], true
}

// File is a document found on disk.
type File struct {
	Path  string // slash-separated, relative to the root
	Kind  string
	Size  int64
	MTime int64 // unix nanoseconds
}

// Matcher decides which paths to skip.
type Matcher struct{ res []*regexp.Regexp }

// NewMatcher compiles ignore globs. "**" matches any number of folders, "*"
// and "?" stay within one path segment.
func NewMatcher(globs []string) (*Matcher, error) {
	m := &Matcher{}
	for _, g := range globs {
		re, err := globRegexp(g)
		if err != nil {
			return nil, err
		}
		m.res = append(m.res, re)
	}
	return m, nil
}

func globRegexp(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Skip reports whether rel (a file, or a folder if dir) is ignored. Hidden
// files and folders (".git", ".ragalay", ".DS_Store") are always skipped.
func (m *Matcher) Skip(rel string, dir bool) bool {
	if strings.HasPrefix(path.Base(rel), ".") && rel != "." {
		return true
	}
	for _, re := range m.res {
		if re.MatchString(rel) || dir && re.MatchString(rel+"/") {
			return true
		}
	}
	return false
}

// Walk lists the documents under the configured folders. Folders that do
// not exist are reported, not fatal.
func Walk(root string, cfg config.Config) (files map[string]File, missing []string, err error) {
	m, err := NewMatcher(cfg.Scan.Ignore)
	if err != nil {
		return nil, nil, err
	}
	files = map[string]File{}
	for _, folder := range config.EffectiveFolders(cfg.Scan.Folders) {
		start := filepath.Join(root, filepath.FromSlash(folder))
		if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
			missing = append(missing, folder)
			continue
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == start {
					return err
				}
				return nil // unreadable entry: skip it, keep scanning
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel != "." && rel != folder && m.Skip(rel, true) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || m.Skip(rel, false) {
				return nil
			}
			kind, ok := KindOf(rel, cfg.Scan.Kinds)
			if !ok {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			files[rel] = File{Path: rel, Kind: kind, Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return files, missing, nil
}
