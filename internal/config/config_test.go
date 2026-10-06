package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func initRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := initRoot(t)
	cfg := Default()
	cfg.Scan.Folders = []string{"docs", "notes/2024"}
	cfg.Pairs = []Pair{{MD: "md", PDF: "pdf"}}
	cfg.Chunk.Tokens = 512
	if err := Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Scan.Folders, cfg.Scan.Folders) || got.Chunk.Tokens != 512 ||
		len(got.Pairs) != 1 || got.Pairs[0] != cfg.Pairs[0] || got.Cache.QueryTTL != cfg.Cache.QueryTTL {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
}

func TestLoadKeepsDefaultsForMissingKeys(t *testing.T) {
	root := initRoot(t)
	if err := os.WriteFile(Path(root), []byte("[chunk]\ntokens = 128\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Chunk.Tokens != 128 || got.Chunk.Overlap != 32 || got.Embed.Dim != 1024 {
		t.Fatalf("defaults not kept: %+v", got)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	root := initRoot(t)
	os.WriteFile(Path(root), []byte("[chunk]\ntokenz = 128\n"), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "tokenz") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := Default()
	cfg.Scan.Folders = []string{"../outside", `docs\win`, "/abs"}
	cfg.Scan.Kinds = []string{"md", "video"}
	cfg.Chunk.Overlap = 300
	cfg.Embed.Dim = 1000
	cfg.Pairs = []Pair{{MD: "md"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"../outside", `docs\\win`, "/abs", "video", "chunk.overlap", "embed.dim", "pairs[0]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestFindRootWalksUp(t *testing.T) {
	root := initRoot(t)
	deep := filepath.Join(root, "a", "b", "c")
	os.MkdirAll(deep, 0o755)
	got, err := FindRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, got, root) {
		t.Fatalf("FindRoot = %s, want %s", got, root)
	}
	if _, err := FindRoot(t.TempDir()); err != ErrNotInitialized {
		t.Fatalf("want ErrNotInitialized, got %v", err)
	}
}

func TestResolveRootExplicit(t *testing.T) {
	root := initRoot(t)
	got, err := ResolveRoot(root)
	if err != nil || !sameDir(t, got, root) {
		t.Fatalf("ResolveRoot(%s) = %s, %v", root, got, err)
	}
	if _, err := ResolveRoot(t.TempDir()); err != ErrNotInitialized {
		t.Fatalf("want ErrNotInitialized for a plain dir, got %v", err)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
