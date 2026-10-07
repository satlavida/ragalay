// Plan 2 Phase 0 spike: EmbeddingGemma 2 GGUF through yzma, parity with the
// full model's query vectors (spikes/gemma/gemma_spike.py).
//
// Usage:
//
//	go run ./gemma/llama -lib <llama.cpp dir> -ref <gemma_queries_cpu.json> -model <gguf> [-model <gguf> ...]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
)

type refFile struct {
	Model       string      `json:"model"`
	QueryPrompt string      `json:"query_prompt"`
	Queries     []string    `json:"queries"`
	Vectors     [][]float64 `json:"vectors"`
}

type models []string

func (m *models) String() string     { return strings.Join(*m, ",") }
func (m *models) Set(s string) error { *m = append(*m, s); return nil }

func main() {
	lib := flag.String("lib", "", "llama.cpp library dir")
	refPath := flag.String("ref", "", "gemma_queries_*.json")
	var ms models
	flag.Var(&ms, "model", "GGUF path (repeatable)")
	flag.Parse()

	var ref refFile
	data, err := os.ReadFile(*refPath)
	check(err)
	check(json.Unmarshal(data, &ref))
	fmt.Printf("reference: %s (%s), prompt %q\n", filepath.Base(*refPath), ref.Model, ref.QueryPrompt)

	os.Setenv("PATH", *lib+string(os.PathListSeparator)+os.Getenv("PATH"))
	check(llama.Load(*lib))
	llama.LogSet(llama.LogSilent())
	llama.Init()
	defer llama.Close()

	for _, model := range ms {
		run(model, ref)
	}
}

func run(model string, ref refFile) {
	t0 := time.Now()
	mp := llama.ModelDefaultParams()
	mp.NGpuLayers = 0
	m, err := llama.ModelLoadFromFile(model, mp)
	check(err)
	defer llama.ModelFree(m)
	cp := llama.ContextDefaultParams()
	cp.NCtx = 2048
	cp.NBatch = 2048
	cp.NUbatch = 2048
	cp.PoolingType = llama.PoolingTypeUnspecified // the model's own (mean for Gemma)
	cp.Embeddings = 1
	ctx, err := llama.InitFromModel(m, cp)
	check(err)
	defer llama.Free(ctx)
	fmt.Printf("\n%s: load %v, n_embd %d, pooling %d\n", filepath.Base(model), time.Since(t0),
		llama.ModelNEmbdOut(m), llama.GetPoolingType(ctx))

	vocab := llama.ModelGetVocab(m)
	n := llama.ModelNEmbdOut(m)
	var cos []float64
	t1 := time.Now()
	for i, q := range ref.Queries {
		mem, err := llama.GetMemory(ctx)
		check(err)
		llama.MemoryClear(mem, true)
		toks := llama.Tokenize(vocab, ref.QueryPrompt+q, true, true)
		ret, err := llama.Decode(ctx, llama.BatchGetOne(toks))
		check(err)
		if ret != 0 {
			panic(fmt.Sprintf("decode ret %d", ret))
		}
		v, err := llama.GetEmbeddingsSeq(ctx, 0, n)
		check(err)
		cos = append(cos, cosine(v, ref.Vectors[i]))
	}
	el := time.Since(t1)
	sort.Float64s(cos)
	var sum float64
	for _, c := range cos {
		sum += c
	}
	fmt.Printf("  %d queries %v (%.1f ms each)\n", len(cos), el, float64(el.Milliseconds())/float64(len(cos)))
	fmt.Printf("  parity: min %.5f  p10 %.5f  mean %.5f  max %.5f\n", cos[0], cos[len(cos)/10], sum/float64(len(cos)), cos[len(cos)-1])
}

func cosine(a []float32, b []float64) float64 {
	var d, na, nb float64
	for i := range b {
		d += float64(a[i]) * b[i]
		na += float64(a[i]) * float64(a[i])
		nb += b[i] * b[i]
	}
	return d / math.Sqrt(na*nb)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
