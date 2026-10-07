package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

// Profile names (plan2 §5.1).
const (
	Gemma2 = "embeddinggemma-2"
	JinaV5 = "jina-v5"
	OpenAI = "openai"

	// DefaultProfile is what new folders use (plan2 P3).
	DefaultProfile = Gemma2
)

// Pooling of a llama.cpp query model.
const (
	PoolLast = "last"
	PoolMean = "mean"
)

// Profile describes one embedding setup: the indexing model, the query model
// and how they are prompted. Local profiles index with the Python sidecar and
// embed queries with a GGUF through llama.cpp; OpenAI does both over HTTP.
type Profile struct {
	Name          string
	Title         string // shown to people
	License       string
	LicenseURL    string
	NonCommercial bool // setup asks to accept the license
	Modalities    []string
	Dims          []int // allowed dims (Matryoshka); nil = any
	DefaultDim    int
	IndexModel    string // Hugging Face model the sidecar loads
	IndexRevision string // pinned revision (the model may run remote code)
	QueryPrefix   string // prepended to queries for the GGUF
	Pooling       string // llama.cpp pooling for the GGUF
	// Pitch is one line for the setup wizard and `model list`.
	Pitch string
}

// Local reports whether the profile runs on this computer (sidecar + GGUF).
func (p Profile) Local() bool { return p.Name != OpenAI }

// AllowsDim reports whether dim is valid for the profile.
func (p Profile) AllowsDim(dim int) bool {
	if p.Dims == nil {
		return dim >= 1 && dim <= 8192
	}
	return slices.Contains(p.Dims, dim)
}

// Embeds reports whether the profile can embed modality m.
func (p Profile) Embeds(m string) bool { return slices.Contains(p.Modalities, m) }

var profiles = []Profile{
	{
		Name: Gemma2, Title: "EmbeddingGemma 2",
		License: "Apache 2.0", LicenseURL: "https://www.apache.org/licenses/LICENSE-2.0",
		Modalities: []string{Text, Image, PDFPage},
		Dims:       []int{768, 512, 256, 128}, DefaultDim: 768,
		IndexModel: "google/embeddinggemma-2", IndexRevision: "914f7f89142e33e77833254d9c9b90c3cef7303b",
		QueryPrefix: "task: search result | query: ", Pooling: PoolMean,
		Pitch: "recommended; free for any use, about 1.8 GB",
	},
	{
		Name: JinaV5, Title: "Jina v5",
		License: "CC BY-NC 4.0", LicenseURL: "https://creativecommons.org/licenses/by-nc/4.0/", NonCommercial: true,
		Modalities: []string{Text, Image, PDFPage},
		Dims:       []int{1024, 768, 512, 256, 128, 64, 32}, DefaultDim: 1024,
		IndexModel: "jinaai/jina-embeddings-v5-omni-small-retrieval", IndexRevision: "e3ae4b6e4af4ec0799cd931aefaff03235b5f9d4",
		// Without the prefix, parity with omni drops to ~0.92 (plan1 §3.1).
		QueryPrefix: "Query: ", Pooling: PoolLast,
		Pitch: "strong text search; non-commercial use only, about 4.9 GB",
	},
	{
		Name: OpenAI, Title: "Another service (OpenAI-compatible)",
		License: "set by the service",
		// Image files only through a provider image extension (plan2 P7, S12).
		Modalities: []string{Text, Image},
		Pitch:      "Ollama, LM Studio, llama-server, vLLM or an online API",
	},
}

// Profiles lists the built-in profiles, default first.
func Profiles() []Profile { return slices.Clone(profiles) }

// Lookup returns the named profile.
func Lookup(name string) (Profile, bool) {
	i := slices.IndexFunc(profiles, func(p Profile) bool { return p.Name == name })
	if i < 0 {
		return Profile{}, false
	}
	return profiles[i], true
}

// ProfileNames lists the profile names.
func ProfileNames() []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}

// HTTPID names the vector space of an OpenAI-compatible endpoint: model,
// host and dim, plus a short hash of anything else that changes the vectors
// (prompts, image mode, request extras). Moving the same model to another
// host is a new space (plan2 S13).
func HTTPID(model, host string, dim int, variant string) string {
	id := fmt.Sprintf("openai/%s@%s:%d", model, host, dim)
	if variant != "" {
		h := sha256.Sum256([]byte(variant))
		id += "#" + hex.EncodeToString(h[:4])
	}
	return id
}
