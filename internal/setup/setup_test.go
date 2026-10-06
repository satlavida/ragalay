package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if len(queryModel.SHA256) != 64 || !strings.Contains(queryModel.URL, "/resolve/78b0ebc") {
		t.Error("query model must be pinned by revision and sha256")
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
	if err != nil || s.Ready() {
		t.Fatalf("empty state: %+v %v", s, err)
	}
	f := filepath.Join(cache, "x")
	os.WriteFile(f, nil, 0o644)
	s = State{Python: f, Sidecar: f, QueryModel: f, LlamaLib: cache, LicenseAccepted: "now", SelfTestPassedAt: "now"}
	if err := s.Save(cache); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(cache)
	if err != nil || !got.Ready() || !got.QueryReady() {
		t.Fatalf("loaded state not ready: %+v %v", got, err)
	}
	os.Remove(f)
	if got.Ready() {
		t.Fatal("state with missing files must not be ready")
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
