// Package llama embeds search queries in-process with llama.cpp (through
// yzma/purego, no CGO) and the Jina v5 text-small GGUF. Its vectors match the
// omni indexing model's text vectors (plan1 §3.1: cosine ≈ 0.9997).
package llama

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hybridgroup/yzma/pkg/llama"
)

// QueryPrefix is required: without it parity with omni drops to ~0.92.
const QueryPrefix = "Query: "

// The llama.cpp library can only be loaded once per process.
var (
	loadOnce sync.Once
	loadErr  error
)

func loadLibrary(libDir string) error {
	loadOnce.Do(func() {
		// On Windows, ggml.dll's sibling DLLs are found through PATH, not the
		// folder ggml.dll was loaded from.
		os.Setenv("PATH", libDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := llama.Load(libDir); err != nil {
			loadErr = fmt.Errorf("load llama.cpp from %s: %w", libDir, err)
			return
		}
		llama.LogSet(llama.LogSilent())
		llama.Init()
	})
	return loadErr
}

// Embedder implements embed.Querier.
type Embedder struct {
	mu    sync.Mutex
	model llama.Model
	ctx   llama.Context
	vocab llama.Vocab
	dim   int32
}

// Open loads the GGUF model on the CPU (GPU gives nothing for short
// queries and adds driver risk; plan1 G4).
func Open(libDir, modelPath string) (*Embedder, error) {
	if err := loadLibrary(libDir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("query model: %w", err)
	}
	mp := llama.ModelDefaultParams()
	mp.NGpuLayers = 0
	m, err := llama.ModelLoadFromFile(modelPath, mp)
	if err != nil {
		return nil, fmt.Errorf("load query model: %w", err)
	}
	if m == 0 {
		return nil, errors.New("load query model: llama.cpp returned no model")
	}
	cp := llama.ContextDefaultParams()
	cp.NCtx = 2048
	cp.NBatch = 2048
	cp.NUbatch = 2048
	cp.PoolingType = llama.PoolingTypeLast
	cp.Embeddings = 1
	ctx, err := llama.InitFromModel(m, cp)
	if err != nil {
		llama.ModelFree(m)
		return nil, fmt.Errorf("init query context: %w", err)
	}
	return &Embedder{model: m, ctx: ctx, vocab: llama.ModelGetVocab(m), dim: llama.ModelNEmbd(m)}, nil
}

// EmbedQuery returns the raw (unnormalized) query embedding; callers Fit it.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty query")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == 0 {
		return nil, errors.New("query embedder is closed")
	}
	mem, err := llama.GetMemory(e.ctx)
	if err != nil {
		return nil, err
	}
	llama.MemoryClear(mem, true)
	toks := llama.Tokenize(e.vocab, QueryPrefix+text, true, true)
	if len(toks) > 2048 {
		toks = toks[:2048]
	}
	ret, err := llama.Decode(e.ctx, llama.BatchGetOne(toks))
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if ret != 0 {
		return nil, fmt.Errorf("embed query: llama.cpp decode returned %d", ret)
	}
	v, err := llama.GetEmbeddingsSeq(e.ctx, 0, e.dim)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return append([]float32(nil), v...), nil
}

// Close frees the model. The library stays loaded for the process lifetime.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx != 0 {
		llama.Free(e.ctx)
		e.ctx = 0
	}
	if e.model != 0 {
		llama.ModelFree(e.model)
		e.model = 0
	}
	return nil
}
