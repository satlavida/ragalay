// Package openai embeds through an OpenAI-compatible /v1/embeddings
// endpoint: a local server (Ollama, LM Studio, llama-server, vLLM, ...) or an
// online service (plan2 §5.3). Images go through a provider extension when
// the service has one (plan2 P7). It is plain net/http: no CGO, no Python.
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/satlavida/ragalay/internal/embed"
)

// Image request shapes (config.ImageJina etc.).
const (
	ImageNone     = "none"
	ImageJina     = "jina"
	ImageVLLM     = "vllm"
	ImageLlamaCpp = "llamacpp"
)

// Options configure a Client.
type Options struct {
	BaseURL    string // ends in /v1
	Model      string
	APIKey     string // sent as a Bearer token when set
	Dimensions int    // >0: ask the service for this many dimensions
	// Dim is the index dimension. Unless Matryoshka is set, every vector
	// must have exactly Dim values (some servers ignore "dimensions").
	Dim            int
	Matryoshka     bool
	ImageInput     string
	QueryPrefix    string
	DocumentPrefix string
	QueryExtra     map[string]any
	DocumentExtra  map[string]any
	BatchSize      int
	Concurrency    int
	MaxInputTokens int
	Timeout        time.Duration // per indexing request
	QueryTimeout   time.Duration // per query
	MaxSide        int           // images are scaled to fit
	HTTP           *http.Client
	// Retries and backoff for 429 and 5xx answers (defaults 5 and 1 s).
	Retries int
	Backoff time.Duration
}

// Client implements embed.Querier and the indexer's EmbedDocuments.
type Client struct {
	o      Options
	mu     sync.Mutex
	float  bool   // the service rejected base64: ask for floats
	marker string // llama-server media marker
}

// New returns a client.
func New(o Options) *Client {
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 64
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.MaxInputTokens <= 0 {
		o.MaxInputTokens = 2000
	}
	if o.Timeout <= 0 {
		o.Timeout = time.Minute
	}
	if o.QueryTimeout <= 0 {
		o.QueryTimeout = 5 * time.Second
	}
	if o.MaxSide <= 0 {
		o.MaxSide = 1024
	}
	if o.Retries == 0 {
		o.Retries = 5
	}
	if o.Backoff == 0 {
		o.Backoff = time.Second
	}
	if o.ImageInput == "" {
		o.ImageInput = ImageNone
	}
	return &Client{o: o}
}

// Close is a no-op (embed.Querier).
func (c *Client) Close() error { return nil }

// Modalities the client can embed.
func (c *Client) Modalities() []string {
	if c.o.ImageInput == ImageNone {
		return []string{embed.Text}
	}
	return []string{embed.Text, embed.Image}
}

// EmbedQuery embeds a search query (embed.Querier). It gives up after
// QueryTimeout so search can fall back to keywords.
func (c *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, c.o.QueryTimeout)
	defer cancel()
	vs, err := c.embedTexts(ctx, []string{c.o.QueryPrefix + c.clip(text)}, c.o.QueryExtra, 1)
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// EmbedDocuments embeds documents in order. Text is batched and sent with
// up to Concurrency requests at a time; images use the image extension.
func (c *Client) EmbedDocuments(ctx context.Context, in []embed.Input) ([][]float32, error) {
	out := make([][]float32, len(in))
	var texts []string
	var at []int
	for i, it := range in {
		switch it.Modality {
		case embed.Text:
			texts = append(texts, c.o.DocumentPrefix+c.clip(it.Text))
			at = append(at, i)
		case embed.Image:
			if c.o.ImageInput == ImageNone {
				return nil, errors.New("this service has no image input configured (embed.openai.image_input)")
			}
			v, err := c.embedImage(ctx, it)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", it.Path, err)
			}
			out[i] = v
		default:
			return nil, fmt.Errorf("%s inputs are not sent to embedding services", it.Modality)
		}
	}
	if len(texts) > 0 {
		vs, err := c.embedTexts(ctx, texts, c.o.DocumentExtra, c.o.Concurrency)
		if err != nil {
			return nil, err
		}
		for j, v := range vs {
			out[at[j]] = v
		}
	}
	return out, nil
}

// clip keeps an input under MaxInputTokens (some servers truncate silently,
// others fail; plan2 §3.1). Tokens are estimated at 3 characters each.
func (c *Client) clip(s string) string {
	limit := c.o.MaxInputTokens * 3
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}

// embedTexts sends texts in batches, at most conc requests at once.
func (c *Client) embedTexts(ctx context.Context, texts []string, extra map[string]any, conc int) ([][]float32, error) {
	out := make([][]float32, len(texts))
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, conc)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for i := 0; i < len(texts); i += c.o.BatchSize {
		from, to := i, min(i+c.o.BatchSize, len(texts))
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			body := c.body(extra)
			body["input"] = texts[from:to]
			vs, err := c.request(ctx, body, to-from)
			if err != nil {
				once.Do(func() { firstErr = err; cancel() }) // the first failure stops the rest
				return
			}
			copy(out[from:to], vs)
		}()
	}
	wg.Wait()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (c *Client) body(extra map[string]any) map[string]any {
	b := map[string]any{"model": c.o.Model}
	for k, v := range extra {
		b[k] = v
	}
	if c.o.Dimensions > 0 {
		b["dimensions"] = c.o.Dimensions
	}
	c.mu.Lock()
	if !c.float {
		b["encoding_format"] = "base64"
	}
	c.mu.Unlock()
	return b
}

// StatusError is an unsuccessful HTTP answer.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("embedding service answered HTTP %d: %s", e.Code, e.Body)
}

// transient reports network failures worth retrying (timeouts, dropped
// connections). A host that does not exist or refuses connections (the
// server is not running) fails at once.
func transient(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTimeout || dns.IsTemporary
	}
	if refused(err) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// refused reports a connection that could not be made (nothing listens,
// or the host is down). Windows reports refusals as WSAECONNREFUSED, which
// is not syscall.ECONNREFUSED, so any failed dial that did not time out
// counts.
func refused(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial" && !op.Timeout()
}

func retryable(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout || code == http.StatusInternalServerError
}

// request posts to /embeddings with retries and returns n vectors.
func (c *Client) request(ctx context.Context, body map[string]any, n int) ([][]float32, error) {
	wait := c.o.Backoff
	for attempt := 0; ; attempt++ {
		vs, retryAfter, err := c.post(ctx, body, n)
		var se *StatusError
		switch {
		case err == nil:
			return vs, nil
		case errors.As(err, &se) && (se.Code == 400 || se.Code == 422) && body["encoding_format"] == "base64":
			// Some services only answer with floats.
			c.mu.Lock()
			c.float = true
			c.mu.Unlock()
			delete(body, "encoding_format")
			continue
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case attempt >= c.o.Retries:
			return nil, err
		case errors.As(err, &se) && !retryable(se.Code):
			return nil, err
		case se == nil && !transient(err):
			return nil, err
		}
		d := wait
		if retryAfter > 0 {
			d = retryAfter
		}
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		wait *= 2
	}
}

type response struct {
	Data []struct {
		Index     int             `json:"index"`
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
}

func (c *Client) post(ctx context.Context, body map[string]any, n int) ([][]float32, time.Duration, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, strings.TrimRight(c.o.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.o.APIKey)
	}
	resp, err := c.o.HTTP.Do(req)
	if err != nil {
		if refused(err) {
			return nil, 0, fmt.Errorf("nothing answers at %s (is the embedding server running?): %w", c.o.BaseURL, err)
		}
		return nil, 0, redact(err, c.o.APIKey)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		var after time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			after = time.Duration(s) * time.Second
		}
		return nil, after, &StatusError{Code: resp.StatusCode, Body: shorten(string(raw))}
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, 0, fmt.Errorf("embedding service sent something that is not an embeddings answer: %s", shorten(string(raw)))
	}
	if len(r.Data) != n {
		return nil, 0, fmt.Errorf("asked for %d embeddings, the service returned %d", n, len(r.Data))
	}
	out := make([][]float32, n)
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= n || out[d.Index] != nil {
			return nil, 0, fmt.Errorf("embedding service returned a bad index %d", d.Index)
		}
		v, err := decodeVector(d.Embedding)
		if err != nil {
			return nil, 0, err
		}
		if err := c.checkDim(len(v)); err != nil {
			return nil, 0, err
		}
		out[d.Index] = v
	}
	return out, 0, nil
}

// ErrDim is a vector size that does not fit the index.
type ErrDim struct{ Got, Want int }

func (e *ErrDim) Error() string {
	return fmt.Sprintf("the service returns %d-dimension vectors but embed.dim is %d; set embed.dim = %d in .ragalay/config.toml (or matryoshka = true if the model allows truncation)",
		e.Got, e.Want, e.Got)
}

func (c *Client) checkDim(n int) error {
	if c.o.Dim == 0 || n == c.o.Dim || (c.o.Matryoshka && n > c.o.Dim) {
		return nil
	}
	return &ErrDim{Got: n, Want: c.o.Dim}
}

// decodeVector reads a float array or base64 little-endian float32s.
func decodeVector(raw json.RawMessage) ([]float32, error) {
	var f []float32
	if json.Unmarshal(raw, &f) == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("embedding is neither a list of numbers nor base64")
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 {
		return nil, errors.New("embedding is not valid base64 float32 data")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

func shorten(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// redact keeps the API key out of error messages (it can appear in URLs
// some clients print).
func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}

// serverRoot is the base URL without its /v1 suffix (llama-server's /props).
func serverRoot(base string) string {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return base
	}
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	return strings.TrimRight(u.String(), "/")
}
