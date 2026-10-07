package openai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// fakeServer answers like an OpenAI-compatible server: vector i of a
// request is [len(text), i+1, 0, ...] (dim 4), as floats or base64.
type fakeServer struct {
	t         *testing.T
	mu        sync.Mutex
	requests  []map[string]any
	noBase64  bool // reject encoding_format=base64 with 400
	fail429   int  // answer 429 this many times first
	wrongN    bool
	delay     time.Duration
	inflight  atomic.Int32
	maxFlight atomic.Int32
	auth      string
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/props" {
		json.NewEncoder(w).Encode(map[string]any{"media_marker": "<__media_X__>", "modalities": map[string]bool{"vision": true}})
		return
	}
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxFlight.Load()
		if n <= m || f.maxFlight.CompareAndSwap(m, n) {
			break
		}
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	f.auth = r.Header.Get("Authorization")
	fail := f.fail429 > 0
	if fail {
		f.fail429--
	}
	f.mu.Unlock()
	time.Sleep(f.delay)
	if fail {
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
		return
	}
	if f.noBase64 && body["encoding_format"] == "base64" {
		http.Error(w, `{"error":"unknown encoding_format"}`, http.StatusBadRequest)
		return
	}
	var items []any
	switch in := body["input"].(type) {
	case []any:
		items = in
	case nil:
		items = []any{body["messages"]}
	default:
		items = []any{in}
	}
	if f.wrongN {
		items = items[1:]
	}
	var data []map[string]any
	for i, it := range items {
		s, _ := it.(string)
		v := []float32{float32(len(s)), float32(i + 1), 0, 0}
		var emb any = v
		if body["encoding_format"] == "base64" {
			b := make([]byte, 16)
			for j, x := range v {
				binary.LittleEndian.PutUint32(b[4*j:], math.Float32bits(x))
			}
			emb = base64.StdEncoding.EncodeToString(b)
		}
		data = append(data, map[string]any{"index": i, "embedding": emb})
	}
	// Answer out of order: the client must sort by index.
	for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data, "model": body["model"]})
}

func newFake(t *testing.T, f *fakeServer, o Options) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	o.BaseURL, o.Model = srv.URL+"/v1", "m"
	o.Backoff = time.Millisecond
	return New(o)
}

func texts(n int) []embed.Input {
	in := make([]embed.Input, n)
	for i := range in {
		in[i] = embed.Input{Modality: embed.Text, Text: strings.Repeat("x", i+1)}
	}
	return in
}

func TestBatchesInOrderWithBase64AndPrefix(t *testing.T) {
	f := &fakeServer{t: t}
	c := newFake(t, f, Options{BatchSize: 3, Concurrency: 2, DocumentPrefix: "doc: ", APIKey: "sk-1",
		DocumentExtra: map[string]any{"task": "retrieval.passage"}, Dimensions: 4})
	vs, err := c.EmbedDocuments(context.Background(), texts(7))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vs {
		if want := float32(len("doc: ") + i + 1); v[0] != want {
			t.Fatalf("vector %d = %v, want first value %v", i, v, want)
		}
	}
	if len(f.requests) != 3 || f.requests[0]["encoding_format"] != "base64" || f.requests[0]["task"] != "retrieval.passage" ||
		f.requests[0]["dimensions"] != float64(4) || f.auth != "Bearer sk-1" {
		t.Fatalf("requests %v auth %q", f.requests, f.auth)
	}
}

func TestFallsBackToFloatsAndRetries429(t *testing.T) {
	f := &fakeServer{t: t, noBase64: true, fail429: 2}
	c := newFake(t, f, Options{})
	if _, err := c.EmbedDocuments(context.Background(), texts(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedDocuments(context.Background(), texts(1)); err != nil {
		t.Fatal(err)
	}
	last := f.requests[len(f.requests)-1]
	if _, ok := last["encoding_format"]; ok {
		t.Fatal("after a base64 rejection the client should ask for floats")
	}
}

func TestErrorsAreReported(t *testing.T) {
	f := &fakeServer{t: t, wrongN: true}
	c := newFake(t, f, Options{})
	if _, err := c.EmbedDocuments(context.Background(), texts(3)); err == nil || !strings.Contains(err.Error(), "returned 2") {
		t.Fatalf("wrong count: %v", err)
	}
	// Nothing listening: fail at once with a hint, no retries.
	down := New(Options{BaseURL: "http://127.0.0.1:1/v1", Model: "m"})
	t0 := time.Now()
	if _, err := down.EmbedDocuments(context.Background(), texts(1)); err == nil || !strings.Contains(err.Error(), "is the embedding server running") || time.Since(t0) > 3*time.Second {
		t.Fatalf("refused connection: %v after %v", err, time.Since(t0))
	}
	f2 := &fakeServer{t: t, fail429: 100}
	c2 := newFake(t, f2, Options{Retries: 2})
	var se *StatusError
	if _, err := c2.EmbedDocuments(context.Background(), texts(1)); !errors.As(err, &se) || se.Code != 429 {
		t.Fatalf("persistent 429: %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	f := &fakeServer{t: t, delay: 30 * time.Millisecond}
	c := newFake(t, f, Options{BatchSize: 1, Concurrency: 2})
	if _, err := c.EmbedDocuments(context.Background(), texts(6)); err != nil {
		t.Fatal(err)
	}
	if m := f.maxFlight.Load(); m != 2 {
		t.Fatalf("max requests in flight %d, want 2", m)
	}
}

func TestQueryTimeoutAndClip(t *testing.T) {
	f := &fakeServer{t: t, delay: 200 * time.Millisecond}
	c := newFake(t, f, Options{QueryTimeout: 20 * time.Millisecond, QueryPrefix: "q: ", Retries: -1})
	if _, err := c.EmbedQuery(context.Background(), "hi"); err == nil {
		t.Fatal("slow service must time out so search can fall back")
	}
	f.delay = 0
	c = newFake(t, f, Options{QueryPrefix: "q: ", MaxInputTokens: 10})
	v, err := c.EmbedQuery(context.Background(), strings.Repeat("y", 100))
	if err != nil || v[0] != float32(len("q: ")+30) {
		t.Fatalf("clipped query: %v %v", v, err)
	}
}

func writePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
	for x := 0; x < 3000; x++ {
		img.Set(x, x%1000, color.RGBA{255, 0, 0, 255})
	}
	p := filepath.Join(t.TempDir(), "a.png")
	fh, _ := os.Create(p)
	png.Encode(fh, img)
	fh.Close()
	return p
}

func TestImageShapes(t *testing.T) {
	img := writePNG(t)
	in := []embed.Input{{Modality: embed.Image, Path: img, Text: "a red line"}}
	for _, mode := range []string{ImageJina, ImageVLLM, ImageLlamaCpp} {
		f := &fakeServer{t: t}
		c := newFake(t, f, Options{ImageInput: mode, MaxSide: 512})
		if _, err := c.EmbedDocuments(context.Background(), in); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		b, _ := json.Marshal(f.requests[0])
		s := string(b)
		switch mode {
		case ImageJina:
			if !strings.Contains(s, `"input":[{"image":"data:image/png;base64,`) {
				t.Errorf("jina shape: %.200s", s)
			}
		case ImageVLLM:
			if !strings.Contains(s, `"image_url":{"url":"data:image/png;base64,`) || !strings.Contains(s, "a red line") {
				t.Errorf("vllm shape: %.200s", s)
			}
		case ImageLlamaCpp:
			if !strings.Contains(s, `"prompt_string":"a red line `) || !strings.Contains(s, "__media_X__") || !strings.Contains(s, `"multimodal_data":["`) {
				t.Errorf("llamacpp shape: %s", s[strings.Index(s, "prompt_string")-1:])
			}
		}
	}
	// Images are scaled to MaxSide before upload.
	data, err := loadImage(img, 512)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := png.DecodeConfig(strings.NewReader(string(data)))
	if cfg.Width != 512 || cfg.Height != 170 {
		t.Fatalf("scaled to %dx%d", cfg.Width, cfg.Height)
	}
	// Without an image extension, images are refused (the indexer keeps
	// them keyword-only instead).
	c := newFake(t, &fakeServer{t: t}, Options{})
	if _, err := c.EmbedDocuments(context.Background(), in); err == nil {
		t.Fatal("image_input none must refuse images")
	}
}

func TestServerRoot(t *testing.T) {
	if got := serverRoot("http://h:8080/v1/"); got != "http://h:8080" {
		t.Fatal(got)
	}
}
