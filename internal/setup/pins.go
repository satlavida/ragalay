package setup

import "github.com/satlavida/ragalay/internal/embed"

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

// llama.cpp CPU build for query embedding (plan1 G4). b11459 is the first
// release line with EmbeddingGemma 2 (b11454) that yzma v1.29.0 binds: its
// llama.h differs from yzma's v0.6.0 only by one enum value (plan2 §3).
// Digests from the GitHub release.
const llamaVersion = "b11459"

var llamaAssets = map[string]Asset{
	winAMD64:    {"https://github.com/ggml-org/llama.cpp/releases/download/b11459/llama-b11459-bin-win-cpu-x64.zip", "3aeb34d2a64eddb10bbe2f022834d5d7428f03103380b110c33d521bbf11a637", 19438390},
	darwinARM64: {"https://github.com/ggml-org/llama.cpp/releases/download/b11459/llama-b11459-bin-macos-arm64.tar.gz", "04cd4ab4fd748af1abc02d8d1759b55121141a377c97c297192e9608de2e8c4e", 12007697},
	darwinAMD64: {"https://github.com/ggml-org/llama.cpp/releases/download/b11459/llama-b11459-bin-macos-x64.tar.gz", "60324b4be8194dcd7e75de204fe6c15cd409f96956417b9edf8ec0548518307a", 11525370},
	linuxAMD64:  {"https://github.com/ggml-org/llama.cpp/releases/download/b11459/llama-b11459-bin-ubuntu-x64.tar.gz", "90a33b328164667853e235d7061ce627dcd684c9651d80bfd69d764484d5f1a7", 17732675},
	linuxARM64:  {"https://github.com/ggml-org/llama.cpp/releases/download/b11459/llama-b11459-bin-ubuntu-arm64.tar.gz", "d0cb5dbd38c3ec0ea1e8d3db23cd57c34438edc1f7e1877256ea2e1b9fd4a32c", 13724736},
}

// QueryModel is a local profile's search model (a GGUF for llama.cpp).
type QueryModel struct {
	Asset
	File string
	ID   string // names the model in the query cache key
}

// Search models per local profile, both Q8_0 (plan1 §3.1, plan2 Phase 0:
// parity 0.9997 for Jina, 0.9999 for Gemma).
var queryModels = map[string]QueryModel{
	embed.JinaV5: {
		Asset: Asset{
			URL:    "https://huggingface.co/jinaai/jina-embeddings-v5-text-small-retrieval-GGUF/resolve/78b0ebcb4c870fdfef409e578b65288b49a4fa90/v5-small-retrieval-Q8_0.gguf",
			SHA256: "b759677362414e664160ffb017fbc74c300feaa4ad4085f69f2cc1bfa12ccb71",
			Size:   639447424,
		},
		File: "v5-small-retrieval-Q8_0.gguf",
		ID:   "jina-embeddings-v5-text-small-retrieval-Q8_0@78b0ebc",
	},
	embed.Gemma2: {
		Asset: Asset{
			URL:    "https://huggingface.co/ggml-org/embeddinggemma-2-GGUF/resolve/bfcd298762cc34d0357ece5ebdd31791a3a374d8/embeddinggemma-2-Q8_0.gguf",
			SHA256: "2188ac1deca4b77dffefd603c2776a9d76d9d74ec01841392982ebb840b09135",
			Size:   309855456,
		},
		File: "embeddinggemma-2-Q8_0.gguf",
		ID:   "embeddinggemma-2-Q8_0@bfcd298",
	},
}

// QueryModelFor returns a local profile's search model.
func QueryModelFor(profile string) (QueryModel, bool) {
	q, ok := queryModels[profile]
	return q, ok
}

// Indexing model weights per local profile (downloaded by the sidecar from
// Hugging Face), for the download-size prompt.
var indexModelSizes = map[string]int64{
	embed.JinaV5: 4_300_000_000,
	embed.Gemma2: 1_488_915_288,
}

// Python environment for the indexing sidecar.
const pythonVersion = "3.11"

// Packages installed after torch, from PyPI. Versions tested in Phase 0.
var pythonPackages = []string{
	"transformers==5.19.0", // 5.19 adds embedding_gemma2 (plan2 Phase 0)
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
