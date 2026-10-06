package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ErrOutsideRoot is returned for folders that are not inside the root (G5).
var ErrOutsideRoot = errors.New("folder is outside the ragalay directory")

// NormalizeFolder turns p (absolute, or relative to cwd) into the stored form:
// slash-separated, relative to root, "." for the root itself. The folder must
// exist and be inside root, and must not be inside .ragalay.
func NormalizeFolder(root, cwd, p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, p)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: not a folder", p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realAbs, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s: %w", p, ErrOutsideRoot)
	}
	rel = filepath.ToSlash(rel)
	if rel == DirName || strings.HasPrefix(rel, DirName+"/") {
		return "", fmt.Errorf("%s: cannot scan ragalay's own %s folder", p, DirName)
	}
	return rel, nil
}

// Covers reports whether folder (stored form) contains rel (a stored,
// slash-separated path relative to the root).
func Covers(folder, rel string) bool {
	if folder == "." {
		return true
	}
	return rel == folder || strings.HasPrefix(rel, folder+"/")
}

// EffectiveFolders returns the folders actually scanned: the configured list,
// or the whole root when the list is empty.
func EffectiveFolders(folders []string) []string {
	if len(folders) == 0 {
		return []string{"."}
	}
	return folders
}

// AddFolder adds f to folders, keeping the list free of overlaps. It returns
// the new list and a human-readable note describing what happened.
func AddFolder(folders []string, f string) ([]string, string) {
	for _, existing := range folders {
		if Covers(existing, f) {
			if existing == f {
				return folders, fmt.Sprintf("%s is already scanned", f)
			}
			return folders, fmt.Sprintf("%s is already covered by %s", f, existing)
		}
	}
	var kept, replaced []string
	for _, existing := range folders {
		if Covers(f, existing) {
			replaced = append(replaced, existing)
		} else {
			kept = append(kept, existing)
		}
	}
	out := append(kept, f)
	slices.Sort(out)
	if len(replaced) > 0 {
		return out, fmt.Sprintf("added %s (replaces %s)", f, strings.Join(replaced, ", "))
	}
	return out, "added " + f
}

// RemoveFolder removes f from folders. It reports false if f was not listed.
func RemoveFolder(folders []string, f string) ([]string, bool) {
	i := slices.Index(folders, path.Clean(f))
	if i < 0 {
		return folders, false
	}
	return slices.Delete(slices.Clone(folders), i, i+1), true
}
