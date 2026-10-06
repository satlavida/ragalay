package setup

// Everything setup downloads is pinned here, by version and SHA-256. To
// upgrade, change the pins and re-run the Phase 0 parity checks.

// Asset is one downloadable archive or file.
type Asset struct {
	URL    string
	SHA256 string
	Size   int64 // approximate, for the download-size prompt
}

// platform keys are GOOS/GOARCH.
const (
	winAMD64    = "windows/amd64"
	darwinARM64 = "darwin/arm64"
	darwinAMD64 = "darwin/amd64"
	linuxAMD64  = "linux/amd64"
	linuxARM64  = "linux/arm64"
)

// uv manages the Python interpreter, venv and packages (G9).
const uvVersion = "0.12.23"

var uvAssets = map[string]Asset{
	winAMD64:    {"https://github.com/astral-sh/uv/releases/download/0.12.23/uv-x86_64-pc-windows-msvc.zip", "75d05de6762778c31ee183398de7dd15093fad0ed90b1f236d8205ea5ec00c90", 18043715},
	darwinARM64: {"https://github.com/astral-sh/uv/releases/download/0.12.23/uv-aarch64-apple-darwin.tar.gz", "50487ae565ccd96e499056b4674d438f4c53170202617b4c759defe0c6a1b544", 17044217},
	darwinAMD64: {"https://github.com/astral-sh/uv/releases/download/0.12.23/uv-x86_64-apple-darwin.tar.gz", "960da44cb4b73685206ddd250b19e0a117fa41095710c1038f081f5cb613efb4", 21263712},
	linuxAMD64:  {"https://github.com/astral-sh/uv/releases/download/0.12.23/uv-x86_64-unknown-linux-gnu.tar.gz", "9167d72b3319674b6303c4cbe071854bba13ebdf3d76b1a7cbdc175471fb66d6", 19906150},
	linuxARM64:  {"https://github.com/astral-sh/uv/releases/download/0.12.23/uv-aarch64-unknown-linux-gnu.tar.gz", "6524bd338177ed50d035d39354e12545e993bbeba2ecbddf0480c5b3a81d313f", 18965616},
}

// llama.cpp CPU build for query embedding (G4). Same release yzma v1.28.0
// pins (v0.5.0 = upstream b11146); digests from its manifest.
const llamaVersion = "b11146"

var llamaAssets = map[string]Asset{
	winAMD64:    {"https://github.com/ggml-org/llama.cpp/releases/download/b11146/llama-b11146-bin-win-cpu-x64.zip", "14cf1303ca9ac3abd94816850532f9f9a69ac66fbaca3776fc6f9061c2fac1d1", 30_000_000},
	darwinARM64: {"https://github.com/ggml-org/llama.cpp/releases/download/b11146/llama-b11146-bin-macos-arm64.tar.gz", "1ad3f9eff80edb9dbef4259ad564d1720612ef7eea48fa4afed0e54f5f3d5711", 30_000_000},
	darwinAMD64: {"https://github.com/ggml-org/llama.cpp/releases/download/b11146/llama-b11146-bin-macos-x64.tar.gz", "305f0e3a17d2c01eb205cd0a62128357f1ec3b55329cb084d94e5ec0115d7a3b", 30_000_000},
	linuxAMD64:  {"https://github.com/ggml-org/llama.cpp/releases/download/b11146/llama-b11146-bin-ubuntu-x64.tar.gz", "c150306eb16b5ab696f76a8bdf810c35fd98a24e82158742e6fa28f420ff8410", 30_000_000},
	linuxARM64:  {"https://github.com/hybridgroup/llama-cpp-builder/releases/download/v0.5.0/llama-v0.5.0-bin-ubuntu-cpu-arm64.tar.gz", "49b34d29905bf91d3d21f25ca4fcffbd3567c3a936737d2a2eb6c169ef9020ca", 30_000_000},
}

// Query model: Jina v5 text-small retrieval, Q8_0 (plan1 §3.1 parity).
var queryModel = struct {
	Asset
	File string
}{
	Asset: Asset{
		URL:    "https://huggingface.co/jinaai/jina-embeddings-v5-text-small-retrieval-GGUF/resolve/78b0ebcb4c870fdfef409e578b65288b49a4fa90/v5-small-retrieval-Q8_0.gguf",
		SHA256: "b759677362414e664160ffb017fbc74c300feaa4ad4085f69f2cc1bfa12ccb71",
		Size:   639447424,
	},
	File: "v5-small-retrieval-Q8_0.gguf",
}

// QueryModelID names the query model for the query cache key.
const QueryModelID = "jina-embeddings-v5-text-small-retrieval-Q8_0@78b0ebc"

// Indexing model weights (downloaded by the sidecar from Hugging Face).
const indexModelSize = 4_300_000_000

// Python environment for the indexing sidecar.
const pythonVersion = "3.11"

// Packages installed after torch, from PyPI. Versions tested in Phase 0.
var pythonPackages = []string{
	"transformers==5.18.0",
	"sentence-transformers==6.1.0",
	"huggingface-hub==1.33.0",
	"pillow==12.3.0",
	"pypdfium2==5.14.0",
	"numpy==2.4.6",
}

// Variant is a torch build for one kind of accelerator.
type Variant struct {
	Name     string   // "cpu", "cuda", "mps", "rocm-gfx1201", ...
	Index    string   // package index for torch ("" = PyPI)
	Packages []string // torch + torchvision specs
	Size     int64    // approximate download size
	Tested   bool     // verified on real hardware in Phase 0
}

var variants = map[string]Variant{
	"cpu": {Name: "cpu", Index: "https://download.pytorch.org/whl/cpu",
		Packages: []string{"torch==2.14.1", "torchvision==0.29.1"}, Size: 400_000_000, Tested: true},
	// macOS wheels on PyPI include MPS support.
	"mps": {Name: "mps", Packages: []string{"torch==2.14.1", "torchvision==0.29.1"}, Size: 400_000_000},
	// cu126 has the widest driver support.
	"cuda": {Name: "cuda", Index: "https://download.pytorch.org/whl/cu126",
		Packages: []string{"torch==2.14.1", "torchvision==0.29.1"}, Size: 3_000_000_000},
	// AMD ROCm on Windows (preview). Tested on the RX 9070 XT (gfx1201).
	"rocm-gfx1201": {Name: "rocm-gfx1201", Index: "https://stable.repo.amd.com/rocm/whl-next/",
		Packages: []string{"torch[device-gfx1201]==2.14.0+rocm10.1.0", "torchvision[device-gfx1201]==0.29.0a0+rocm10.1.0"},
		Size:     3_500_000_000, Tested: true},
	"rocm-gfx1200": {Name: "rocm-gfx1200", Index: "https://stable.repo.amd.com/rocm/whl-next/",
		Packages: []string{"torch[device-gfx1200]==2.14.0+rocm10.1.0", "torchvision[device-gfx1200]==0.29.0a0+rocm10.1.0"},
		Size:     3_500_000_000},
}
