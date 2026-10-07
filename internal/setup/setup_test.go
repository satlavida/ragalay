package setup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/satlavida/ragalay/internal/embed"
)

var allPlatforms = []string{winAMD64, darwinARM64, darwinAMD64, linuxAMD64, linuxARM64}

func TestPinsCoverEveryPlatform(t *testing.T) {
	for _, p := range allPlatforms {
		for name, assets := range map[string]map[string]Asset{"uv": uvAssets, "llama.cpp": llamaAssets} {
			a, ok := assets[p]
			if !ok {
				t.Errorf("%s: no %s asset", p, name)
				continue
			}
			if len(a.SHA256) != 64 || !strings.HasPrefix(a.URL, "https://") {
				t.Errorf("%s %s: bad pin %+v", p, name, a)
			}
		}
	}
	for _, p := range embed.Profiles() {
		if !p.Local() {
			continue
		}
		q, ok := queryModels[p.Name]
		if !ok || len(q.SHA256) != 64 || !regexp.MustCompile(`/resolve/[0-9a-f]{40}/`).MatchString(q.URL) || q.ID == "" {
			t.Errorf("%s: query model must be pinned by revision and sha256: %+v", p.Name, q)
		}
		if indexModelSizes[p.Name] == 0 || len(p.IndexRevision) != 40 {
			t.Errorf("%s: indexing model must be pinned by revision", p.Name)
		}
	}
	for name, v := range variants {
		if v.Name != name || len(v.Packages) != 2 {
			t.Errorf("variant %s malformed: %+v", name, v)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		goos, goarch, smi string
		gpus              []string
		want              string
	}{
		{"windows", "amd64", "GPU 0: NVIDIA GeForce RTX 4090 (UUID: x)", nil, "cuda"},
		{"linux", "amd64", "GPU 0: NVIDIA A100", nil, "cuda"},
		{"darwin", "arm64", "", nil, "mps"},
		{"darwin", "amd64", "", nil, "cpu"},
		{"windows", "amd64", "", []string{"AMD Radeon RX 9070 XT"}, "rocm-gfx1201"},
		{"windows", "amd64", "", []string{"Microsoft Basic Display", "AMD Radeon RX 9060 XT"}, "rocm-gfx1200"},
		{"windows", "amd64", "", []string{"AMD Radeon RX 6800"}, "cpu"},
		{"linux", "amd64", "", []string{"VGA: AMD Radeon RX 9070"}, "cpu"},
	}
	for _, c := range cases {
		got := detect(c.goos, c.goarch, c.smi, c.gpus)
		if got.Variant != c.want || got.Reason == "" {
			t.Errorf("detect(%s/%s, %q, %v) = %+v, want %s", c.goos, c.goarch, c.smi, c.gpus, got, c.want)
		}
	}
}

func TestStateRoundTripAndReady(t *testing.T) {
	cache := t.TempDir()
	s, err := LoadState(cache)
	if err != nil || s.Ready(embed.Gemma2) {
		t.Fatalf("empty state: %+v %v", s, err)
	}
	if !s.Ready(embed.OpenAI) {
		t.Fatal("a service profile needs nothing installed")
	}
	f := filepath.Join(cache, "x")
	os.WriteFile(f, nil, 0o644)
	gemma, _ := embed.Lookup(embed.Gemma2)
	s = State{Python: f, Sidecar: f, LlamaLib: cache, LlamaVersion: llamaVersion}
	s.SetProfile(embed.Gemma2, ProfileState{IndexModel: gemma.IndexModel + "@" + gemma.IndexRevision,
		QueryModel: f, SelfTestPassedAt: "now"})
	if err := s.Save(cache); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(cache)
	if err != nil || !got.Ready(embed.Gemma2) || !got.QueryReady(embed.Gemma2) {
		t.Fatalf("loaded state not ready: %+v %v", got, err)
	}
	if got.Ready(embed.JinaV5) {
		t.Fatal("jina was never set up")
	}
	got.LlamaVersion = "b11146"
	if got.Ready(embed.Gemma2) {
		t.Fatal("an older llama.cpp than the pin must not be ready")
	}
	got.LlamaVersion = llamaVersion
	os.Remove(f)
	if got.Ready(embed.Gemma2) {
		t.Fatal("state with missing files must not be ready")
	}
}

// A Plan 1 state.json (one Jina model) loads as the jina-v5 profile.
func TestLoadStateMigratesPlan1(t *testing.T) {
	cache := t.TempDir()
	plan1 := `{"python": "p", "index_model": "jinaai/jina-embeddings-v5-omni-small-retrieval@e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4",
		"llama_version": "b11146", "query_model": "q.gguf", "license_accepted_at": "2026-10-06T09:48:38Z",
		"self_test_parity": 0.9997, "self_test_passed_at": "2026-10-06T09:50:46Z"}`
	os.WriteFile(filepath.Join(cache, "state.json"), []byte(plan1), 0o644)
	s, err := LoadState(cache)
	if err != nil {
		t.Fatal(err)
	}
	j := s.Profile(embed.JinaV5)
	if j.QueryModel != "q.gguf" || j.LicenseAccepted == "" || j.SelfTestParity != 0.9997 || s.IndexModel != "" {
		t.Fatalf("not migrated: %+v", s)
	}
}

func TestPackagesHashChangesWithPins(t *testing.T) {
	a := packagesHash(variants["cpu"])
	if a == packagesHash(variants["cuda"]) {
		t.Fatal("variants must hash differently")
	}
	old := pythonPackages
	pythonPackages = append([]string{"extra==1"}, old...)
	defer func() { pythonPackages = old }()
	if a == packagesHash(variants["cpu"]) {
		t.Fatal("changing package pins must change the hash")
	}
}

func TestFindFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a", "build", "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "build", "bin", "LLAMA.DLL"), nil, 0o644)
	got, err := findFile(dir, "llama.dll")
	if err != nil || filepath.Base(got) != "LLAMA.DLL" {
		t.Fatalf("findFile = %q, %v", got, err)
	}
	if _, err := findFile(dir, "missing"); err == nil {
		t.Fatal("missing file should fail")
	}
}
