// Phase 0 spike: query embedding with the Jina v5 text-small GGUF.
//
// Compares yzma (llama.cpp in-process via purego) with a llama-server
// subprocess, and checks cosine parity against omni-small query vectors.
//
// Usage:
//
//	go run ./spikes/llama -lib <llama.cpp dir> -model <gguf> -omni <omni_queries.json> [-mode yzma|server|both] [-gpu 99]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
)

type omniFile struct {
	Queries    []string    `json:"queries"`
	Vectors    [][]float64 `json:"vectors"`
	DocChunks  []string    `json:"doc_chunks"`
	DocVectors [][]float64 `json:"doc_vectors"`
}

func main() {
	lib := flag.String("lib", "", "llama.cpp library dir")
	model := flag.String("model", "", "GGUF path")
	omniPath := flag.String("omni", "", "omni_queries.json from the omni spike")
	mode := flag.String("mode", "both", "yzma | server | both")
	gpu := flag.Int("gpu", 99, "GPU layers (0 = CPU)")
	prefix := flag.String("prefix", "Query: ", "query prefix")
	flag.Parse()

	var omni omniFile
	data, err := os.ReadFile(*omniPath)
	check(err)
	check(json.Unmarshal(data, &omni))
	inputs := make([]string, len(omni.Queries))
	for i, q := range omni.Queries {
		inputs[i] = *prefix + q
	}
	fmt.Printf("model: %s  gpu layers: %d  prefix: %q\n", filepath.Base(*model), *gpu, *prefix)

	if *mode == "yzma" || *mode == "both" {
		vecs := runYzma(*lib, *model, *gpu, inputs)
		report("yzma", vecs, omni)
	}
	if *mode == "server" || *mode == "both" {
		vecs := runServer(*lib, *model, *gpu, inputs)
		report("llama-server", vecs, omni)
	}
}

func runYzma(lib, model string, gpu int, inputs []string) [][]float32 {
	t0 := time.Now()
	// Windows resolves ggml.dll's sibling DLLs via PATH, not the DLL's own dir.
	os.Setenv("PATH", lib+string(os.PathListSeparator)+os.Getenv("PATH"))
	check(llama.Load(lib))
	llama.LogSet(llama.LogSilent())
	llama.Init()
	defer llama.Close()
	mp := llama.ModelDefaultParams()
	mp.NGpuLayers = int32(gpu)
	m, err := llama.ModelLoadFromFile(model, mp)
	check(err)
	defer llama.ModelFree(m)
	cp := llama.ContextDefaultParams()
	cp.NCtx = 2048
	cp.NBatch = 2048
	cp.PoolingType = llama.PoolingTypeLast
	cp.Embeddings = 1
	ctx, err := llama.InitFromModel(m, cp)
	check(err)
	defer llama.Free(ctx)
	fmt.Printf("yzma: load lib+model+ctx %v\n", time.Since(t0))

	vocab := llama.ModelGetVocab(m)
	n := llama.ModelNEmbd(m)
	out := make([][]float32, len(inputs))
	var first time.Duration
	t1 := time.Now()
	for i, s := range inputs {
		ti := time.Now()
		mem, err := llama.GetMemory(ctx)
		check(err)
		llama.MemoryClear(mem, true)
		toks := llama.Tokenize(vocab, s, true, true)
		ret, err := llama.Decode(ctx, llama.BatchGetOne(toks))
		check(err)
		if ret != 0 {
			panic(fmt.Sprintf("decode ret %d", ret))
		}
		v, err := llama.GetEmbeddingsSeq(ctx, 0, n)
		check(err)
		out[i] = normalize(append([]float32(nil), v...))
		if i == 0 {
			first = time.Since(ti)
		}
	}
	fmt.Printf("yzma: first query %v, %d queries %v (%.1f ms/query)\n", first, len(inputs), time.Since(t1),
		float64(time.Since(t1).Milliseconds())/float64(len(inputs)))
	return out
}

func runServer(lib, model string, gpu int, inputs []string) [][]float32 {
	port := freePort()
	t0 := time.Now()
	cmd := exec.Command(filepath.Join(lib, "llama-server.exe"), "-m", model, "--embedding", "--pooling", "last",
		"--host", "127.0.0.1", "--port", fmt.Sprint(port), "-ngl", fmt.Sprint(gpu), "-c", "2048", "-ub", "2048", "--log-disable")
	check(cmd.Start())
	defer cmd.Process.Kill()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for {
		r, err := http.Get(base + "/health")
		if err == nil && r.StatusCode == 200 {
			r.Body.Close()
			break
		}
		if r != nil {
			r.Body.Close()
		}
		if time.Since(t0) > 60*time.Second {
			panic("llama-server did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("llama-server: start until healthy %v\n", time.Since(t0))

	out := make([][]float32, len(inputs))
	var first time.Duration
	t1 := time.Now()
	for i, s := range inputs {
		ti := time.Now()
		body, _ := json.Marshal(map[string]any{"input": s})
		r, err := http.Post(base+"/v1/embeddings", "application/json", bytes.NewReader(body))
		check(err)
		var res struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		check(json.NewDecoder(r.Body).Decode(&res))
		r.Body.Close()
		out[i] = normalize(res.Data[0].Embedding)
		if i == 0 {
			first = time.Since(ti)
		}
	}
	fmt.Printf("llama-server: first query %v, %d queries %v\n", first, len(inputs), time.Since(t1))
	return out
}

func report(name string, vecs [][]float32, omni omniFile) {
	if len(omni.Vectors) == 0 {
		return
	}
	sims := make([]float64, len(vecs))
	for i := range vecs {
		sims[i] = cos(vecs[i], omni.Vectors[i])
	}
	sort.Float64s(sims)
	var sum float64
	for _, s := range sims {
		sum += s
	}
	fmt.Printf("%s parity vs omni query vectors: min %.4f  median %.4f  mean %.4f\n",
		name, sims[0], sims[len(sims)/2], sum/float64(len(sims)))

	// Retrieval agreement: top-1 doc chunk with GGUF query vs omni query vector.
	if len(omni.DocVectors) > 0 {
		agree := 0
		for i := range vecs {
			if argmaxDoc(f64(vecs[i]), omni.DocVectors) == argmaxDoc(omni.Vectors[i], omni.DocVectors) {
				agree++
			}
		}
		fmt.Printf("%s top-1 doc agreement with omni: %d/%d\n", name, agree, len(vecs))
	}
}

func argmaxDoc(q []float64, docs [][]float64) int {
	best, bi := -2.0, -1
	for i, d := range docs {
		var s float64
		for j := range q {
			s += q[j] * d[j]
		}
		if s > best {
			best, bi = s, i
		}
	}
	return bi
}

func f64(v []float32) []float64 {
	o := make([]float64, len(v))
	for i, x := range v {
		o[i] = float64(x)
	}
	return o
}

func cos(a []float32, b []float64) float64 {
	var d, na, nb float64
	for i := range a {
		d += float64(a[i]) * b[i]
		na += float64(a[i]) * float64(a[i])
		nb += b[i] * b[i]
	}
	return d / math.Sqrt(na*nb)
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x * x)
	}
	n := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= n
	}
	return v
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
