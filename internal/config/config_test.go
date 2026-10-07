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
	if got.Chunk.Tokens != 128 || got.Chunk.Overlap != 32 || got.Embed.Dim != 768 || got.Embed.Profile != "embeddinggemma-2" {
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

// A Plan 1 config keeps Jina and its exact vector space, so the folder is
// not re-embedded (plan2 §5.2).
func TestLoadMigratesPlan1Embed(t *testing.T) {
	root := initRoot(t)
	plan1 := `[embed]
dim = 512
index_model = "jinaai/jina-embeddings-v5-omni-small-retrieval"
index_revision = "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4"
query_model = "jinaai/jina-embeddings-v5-text-small-retrieval-GGUF:Q8_0"
image_max_side = 1024
`
	if err := os.WriteFile(Path(root), []byte(plan1), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Embed.Profile != "jina-v5" || got.Embed.Dim != 512 || got.Embed.IndexModel != "" {
		t.Fatalf("not migrated: %+v", got.Embed)
	}
	if id := got.Embed.SpaceID(); id != "jinaai/jina-embeddings-v5-omni-small-retrieval@e3ae4b6:512" {
		t.Fatalf("Jina space changed: %s", id)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, got); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path(root))
	if strings.Contains(string(b), "index_model") || !strings.Contains(string(b), `profile = "jina-v5"`) {
		t.Fatalf("saved config:\n%s", b)
	}
}

func TestOpenAISectionDefaultsAndSpace(t *testing.T) {
	root := initRoot(t)
	body := `[embed]
profile = "openai"
dim = 768

[embed.openai]
base_url = "http://127.0.0.1:11434/v1"
model = "nomic-embed-text"
`
	os.WriteFile(Path(root), []byte(body), 0o644)
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	o := got.Embed.OpenAI
	if o.BatchSize != 64 || o.QueryTimeout.Duration == 0 || o.ImageInput != ImageNone {
		t.Fatalf("defaults not filled: %+v", o)
	}
	if !o.Loopback() || o.EffectiveConcurrency() != 4 || got.Embed.Remote() {
		t.Fatal("127.0.0.1 is local")
	}
	if id := got.Embed.SpaceID(); id != "openai/nomic-embed-text@127.0.0.1:11434:768" {
		t.Fatalf("space %s", id)
	}
	o.QueryPrefix = "search_query: "
	if id := got.Embed.SpaceID(); !strings.HasPrefix(id, "openai/nomic-embed-text@127.0.0.1:11434:768#") {
		t.Fatalf("prefix should change the space: %s", id)
	}
	o.BaseURL = "https://api.example.com/v1"
	if o.Loopback() || o.EffectiveConcurrency() != 1 || !got.Embed.Remote() {
		t.Fatal("api.example.com is remote")
	}
}

func TestValidateEmbed(t *testing.T) {
	cfg := Default()
	cfg.Embed.Profile = "nope"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "embed.profile") {
		t.Fatalf("unknown profile: %v", err)
	}
	cfg.Embed, _ = Default().Embed.UseProfile("embeddinggemma-2", 1024)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "embed.dim") {
		t.Fatalf("gemma has no 1024: %v", err)
	}
	cfg.Embed, _ = Default().Embed.UseProfile("openai", 1536)
	cfg.Embed.OpenAI.APIKeyEnv = "sk-abc 123"
	cfg.Embed.OpenAI.BaseURL = "ftp://x"
	cfg.Embed.OpenAI.ImageInput = "png"
	err := cfg.Validate()
	for _, want := range []string{"api_key_env", "base_url", "image_input"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %s error, got %v", want, err)
		}
	}
}
