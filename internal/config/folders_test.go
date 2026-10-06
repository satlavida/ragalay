package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeFolder(t *testing.T) {
	root := initRoot(t)
	for _, d := range []string{"docs/sub", "notes"} {
		os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "file.md"), nil, 0o644)

	cases := []struct {
		cwd, in, want, errHas string
	}{
		{root, "docs", "docs", ""},
		{root, "docs/sub", "docs/sub", ""},
		{root, filepath.Join(root, "notes"), "notes", ""},
		{filepath.Join(root, "docs"), "sub", "docs/sub", ""},
		{filepath.Join(root, "docs"), "..", ".", ""},
		{root, ".", ".", ""},
		{root, "docs/../notes", "notes", ""},
		{root, outside, "", "outside"},
		{root, "..", "", "outside"},
		{root, "missing", "", "missing"},
		{root, "file.md", "", "not a folder"},
		{root, DirName, "", "own"},
	}
	for _, c := range cases {
		got, err := NormalizeFolder(root, c.cwd, c.in)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("NormalizeFolder(%q) err = %v, want containing %q", c.in, err, c.errHas)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("NormalizeFolder(cwd=%s, %q) = %q, %v; want %q", c.cwd, c.in, got, err, c.want)
		}
	}
	if _, err := NormalizeFolder(root, root, outside); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("outside folder should wrap ErrOutsideRoot, got %v", err)
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		folder, rel string
		want        bool
	}{
		{".", "anything/at/all.md", true},
		{"docs", "docs", true},
		{"docs", "docs/a.md", true},
		{"docs", "docsX/a.md", false},
		{"docs/sub", "docs/a.md", false},
	} {
		if got := Covers(c.folder, c.rel); got != c.want {
			t.Errorf("Covers(%q, %q) = %v", c.folder, c.rel, got)
		}
	}
}

func TestAddFolder(t *testing.T) {
	got, note := AddFolder(nil, "docs")
	if !slices.Equal(got, []string{"docs"}) || note != "added docs" {
		t.Fatalf("add to empty: %v %q", got, note)
	}
	got, note = AddFolder([]string{"docs"}, "docs/sub")
	if !slices.Equal(got, []string{"docs"}) || !strings.Contains(note, "covered by docs") {
		t.Fatalf("child of existing: %v %q", got, note)
	}
	got, note = AddFolder([]string{"docs/a", "docs/b", "notes"}, "docs")
	if !slices.Equal(got, []string{"docs", "notes"}) || !strings.Contains(note, "replaces docs/a, docs/b") {
		t.Fatalf("parent of existing: %v %q", got, note)
	}
	got, note = AddFolder([]string{"docs"}, "docs")
	if !slices.Equal(got, []string{"docs"}) || !strings.Contains(note, "already scanned") {
		t.Fatalf("duplicate: %v %q", got, note)
	}
}

func TestRemoveFolderAndEffective(t *testing.T) {
	got, ok := RemoveFolder([]string{"docs", "notes"}, "docs")
	if !ok || !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("remove: %v %v", got, ok)
	}
	if _, ok := RemoveFolder([]string{"notes"}, "docs"); ok {
		t.Fatal("removing an unlisted folder should report false")
	}
	if !slices.Equal(EffectiveFolders(nil), []string{"."}) {
		t.Fatal("empty list should mean the whole root")
	}
}
