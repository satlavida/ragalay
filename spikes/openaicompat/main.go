// Plan 2 Phase 0 spike: how an OpenAI-compatible /v1/embeddings server
// behaves on the things ragalay's client depends on.
//
// Usage:
//
//	go run ./openaicompat -base http://127.0.0.1:11434/v1 -model nomic-embed-text [-key-env OPENAI_API_KEY] [-image spikes/fixtures/car.jpg]
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	base   = flag.String("base", "", "base URL ending in /v1")
	model  = flag.String("model", "", "model name")
	keyEnv = flag.String("key-env", "", "env var holding the API key")
	image  = flag.String("image", "", "image file for the image-input probes")
)

type resp struct {
	Data []struct {
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
	Model string          `json:"model"`
	Usage json.RawMessage `json:"usage"`
}

func post(body map[string]any) (int, []byte, time.Duration) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", strings.TrimRight(*base, "/")+"/embeddings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if *keyEnv != "" {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(*keyEnv))
	}
	t0 := time.Now()
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(t0)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return r.StatusCode, out, time.Since(t0)
}

func decode(raw json.RawMessage) ([]float32, string) {
	var f []float32
	if json.Unmarshal(raw, &f) == nil {
		return f, "float"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		b, err := base64.StdEncoding.DecodeString(s)
		if err == nil && len(b)%4 == 0 {
			v := make([]float32, len(b)/4)
			for i := range v {
				v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
			}
			return v, "base64"
		}
	}
	return nil, "unknown"
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func short(b []byte) string {
	s := strings.ReplaceAll(string(b), "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func probe(name string, body map[string]any) (*resp, []float32) {
	code, out, dt := post(body)
	if code != 200 {
		fmt.Printf("%-28s HTTP %d in %v: %s\n", name, code, dt.Round(time.Millisecond), short(out))
		return nil, nil
	}
	var r resp
	if err := json.Unmarshal(out, &r); err != nil || len(r.Data) == 0 {
		fmt.Printf("%-28s 200 but unparseable: %s\n", name, short(out))
		return nil, nil
	}
	v, enc := decode(r.Data[0].Embedding)
	fmt.Printf("%-28s ok %v: n=%d dim=%d enc=%s |v|=%.4f model=%q usage=%s\n", name, dt.Round(time.Millisecond),
		len(r.Data), len(v), enc, norm(v), r.Model, short(r.Usage))
	return &r, v
}

func main() {
	flag.Parse()
	fmt.Printf("== %s  model %s\n", *base, *model)
	probe("string input", map[string]any{"model": *model, "input": "hello world"})
	_, f := probe("array input", map[string]any{"model": *model, "input": []string{"hello world", "second"}})
	_, b := probe("base64", map[string]any{"model": *model, "input": []string{"hello world"}, "encoding_format": "base64"})
	if f != nil && b != nil && len(f) == len(b) {
		var d float64
		for i := range f {
			d = math.Max(d, math.Abs(float64(f[i]-b[i])))
		}
		fmt.Printf("%-28s max |float-base64| = %g\n", "", d)
	}
	probe("dimensions=256", map[string]any{"model": *model, "input": []string{"hello world"}, "dimensions": 256})
	batch := make([]string, 64)
	for i := range batch {
		batch[i] = fmt.Sprintf("chunk number %d about embeddings and retrieval", i)
	}
	if r, _ := probe("batch of 64", map[string]any{"model": *model, "input": batch}); r != nil {
		ordered := true
		for i, d := range r.Data {
			ordered = ordered && d.Index == i
		}
		fmt.Printf("%-28s indexes in order: %v\n", "", ordered)
	}
	long := strings.Repeat("retrieval augmented generation ", 4000) // ~12k tokens
	probe("~12k-token input", map[string]any{"model": *model, "input": []string{long}})
	probe("unknown model", map[string]any{"model": "no-such-model-xyz", "input": "hi"})
	probe("extra_body field", map[string]any{"model": *model, "input": []string{"hi"}, "task": "retrieval.passage"})
	if *image != "" {
		data, err := os.ReadFile(*image)
		if err != nil {
			panic(err)
		}
		uri := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
		probe("image (jina shape)", map[string]any{"model": *model, "input": []map[string]string{{"image": uri}}})
		probe("image (vllm messages)", map[string]any{"model": *model, "messages": []map[string]any{{
			"role": "user", "content": []map[string]any{{"type": "image_url", "image_url": map[string]string{"url": uri}}},
		}}})
	}
}
