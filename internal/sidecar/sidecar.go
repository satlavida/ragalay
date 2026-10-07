// Package sidecar runs and talks to the Python indexing process
// (sidecar.py). It is only used while indexing; search never starts it.
package sidecar

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// Script is the sidecar source, written to the cache by setup.
//
//go:embed sidecar.py
var Script []byte

// Options describe how to start the sidecar.
type Options struct {
	Python     string // venv python executable
	ScriptPath string // sidecar.py on disk
	Model      string
	Revision   string
	Device     string // "auto", "cpu", "cuda", "mps"
	MaxSide    int
	Env        []string  // extra environment (e.g. HF_HOME)
	Log        io.Writer // sidecar stderr; nil discards
	// StartTimeout bounds model loading. Loading takes a few seconds once
	// cached but a cold disk or first ROCm init can take a minute.
	StartTimeout time.Duration
}

// Info is what the sidecar reports once the model is loaded.
type Info struct {
	Model      string `json:"model"`
	Revision   string `json:"revision"`
	Device     string `json:"device"`
	DeviceName string `json:"device_name"`
	DType      string `json:"dtype"`
	Dim        int    `json:"dim"`
	Torch      string `json:"torch"`
}

// ErrDied means the sidecar process exited unexpectedly.
var ErrDied = errors.New("indexing process stopped unexpectedly (see .ragalay/logs/sidecar.log)")

// Client is a running sidecar. Calls are serialized: the model handles one
// request at a time anyway.
type Client struct {
	opts Options
	mu   sync.Mutex
	proc *process
	info Info
	id   int
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	done   chan struct{}
}

// Start launches the sidecar and waits until the model is loaded.
func Start(ctx context.Context, opts Options) (*Client, error) {
	if opts.StartTimeout == 0 {
		opts.StartTimeout = 3 * time.Minute
	}
	if opts.Device == "" {
		opts.Device = "auto"
	}
	if opts.MaxSide == 0 {
		opts.MaxSide = 1024
	}
	c := &Client{opts: opts}
	if err := c.start(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) start(ctx context.Context) error {
	o := c.opts
	cmd := exec.Command(o.Python, o.ScriptPath, "serve",
		"--model", o.Model, "--revision", o.Revision,
		"--device", o.Device, "--max-side", strconv.Itoa(o.MaxSide))
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8", "HF_HUB_OFFLINE=1",
		"TOKENIZERS_PARALLELISM=false", "TRANSFORMERS_VERBOSITY=error")
	cmd.Env = append(cmd.Env, o.Env...)
	cmd.Stderr = o.Log
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start indexing process: %w", err)
	}
	p := &process{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20), done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.done) }()

	type ready struct {
		Ready bool   `json:"ready"`
		Fatal string `json:"fatal"`
		Info
	}
	got := make(chan error, 1)
	var r ready
	go func() {
		line, err := p.stdout.ReadBytes('\n')
		if err != nil {
			got <- ErrDied
			return
		}
		if err := json.Unmarshal(line, &r); err != nil {
			got <- fmt.Errorf("indexing process said %q: %w", line, err)
			return
		}
		got <- nil
	}()
	timer := time.NewTimer(o.StartTimeout)
	defer timer.Stop()
	select {
	case err = <-got:
	case <-timer.C:
		err = fmt.Errorf("indexing model did not load within %v", o.StartTimeout)
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil && r.Fatal != "" {
		err = fmt.Errorf("indexing process failed to start: %s", r.Fatal)
	} else if err == nil && !r.Ready {
		err = errors.New("indexing process sent an unexpected first message")
	}
	if err != nil {
		p.kill()
		return err
	}
	c.proc, c.info = p, r.Info
	return nil
}

func (p *process) kill() {
	p.stdin.Close()
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	<-p.done
}

// Info returns what the sidecar reported at startup.
func (c *Client) Info() Info { return c.info }

// Call sends one request and decodes the result into out. If the process has
// died it is restarted once and the call retried.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.call(ctx, method, params, out)
	if errors.Is(err, ErrDied) && ctx.Err() == nil {
		if rerr := c.start(ctx); rerr != nil {
			return fmt.Errorf("%w; restart failed: %v", err, rerr)
		}
		err = c.call(ctx, method, params, out)
	}
	return err
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	p := c.proc
	if p == nil {
		return ErrDied
	}
	c.id++
	req, err := json.Marshal(map[string]any{"id": c.id, "method": method, "params": params})
	if err != nil {
		return err
	}
	type response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	got := make(chan error, 1)
	var resp response
	go func() {
		if _, err := p.stdin.Write(append(req, '\n')); err != nil {
			got <- ErrDied
			return
		}
		for {
			line, err := p.stdout.ReadBytes('\n')
			if err != nil {
				got <- ErrDied
				return
			}
			if err := json.Unmarshal(line, &resp); err != nil {
				got <- fmt.Errorf("bad reply from indexing process: %w", err)
				return
			}
			if resp.ID == c.id {
				got <- nil
				return
			}
		}
	}()
	select {
	case err = <-got:
	case <-ctx.Done():
		// Python cannot be interrupted mid-call; stop it and restart on next use.
		p.kill()
		c.proc = nil
		<-got
		return ctx.Err()
	}
	if errors.Is(err, ErrDied) {
		p.kill()
		c.proc = nil
		return err
	}
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// EmbedDocuments implements embed.Indexer. Vectors come back L2-normalized at
// the model's full dimension; callers Fit them to the configured dim.
func (c *Client) EmbedDocuments(ctx context.Context, in []embed.Input) ([][]float32, error) {
	var res struct {
		Vectors []string `json:"vectors"`
	}
	if err := c.Call(ctx, "embed_documents", map[string]any{"inputs": in}, &res); err != nil {
		return nil, err
	}
	if len(res.Vectors) != len(in) {
		return nil, fmt.Errorf("asked for %d vectors, got %d", len(in), len(res.Vectors))
	}
	return decodeAll(res.Vectors)
}

// EmbedQuery embeds a query with the indexing model. Search uses llama.cpp
// instead; this exists for the setup self-test and query-by-example.
func (c *Client) EmbedQuery(ctx context.Context, in embed.Input) ([]float32, error) {
	var res struct {
		Vectors []string `json:"vectors"`
	}
	if err := c.Call(ctx, "embed_query", map[string]any{"input": in}, &res); err != nil {
		return nil, err
	}
	vs, err := decodeAll(res.Vectors)
	if err != nil || len(vs) != 1 {
		return nil, fmt.Errorf("embed_query: bad reply: %v", err)
	}
	return vs[0], nil
}

func decodeAll(b64 []string) ([][]float32, error) {
	out := make([][]float32, len(b64))
	for i, s := range b64 {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
		if out[i], err = embed.FromBlob(raw); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Close asks the sidecar to exit, killing it if it does not.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.proc
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.call(ctx, "shutdown", nil, nil)
	c.proc = nil
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.kill()
	}
	return nil
}

// Download fetches the model weights with the sidecar's download command.
// cacheDir is reported as soon as it is known so callers can show progress
// by watching its size.
func Download(ctx context.Context, opts Options, cacheDir func(string)) error {
	cmd := exec.CommandContext(ctx, opts.Python, opts.ScriptPath, "download",
		"--model", opts.Model, "--revision", opts.Revision)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8", "HF_HUB_DISABLE_PROGRESS_BARS=1")
	cmd.Env = append(cmd.Env, opts.Env...)
	cmd.Stderr = opts.Log
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	var fatal string
	done := false
	for sc.Scan() {
		var msg struct {
			CacheDir string `json:"cache_dir"`
			Done     bool   `json:"done"`
			Fatal    string `json:"fatal"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.CacheDir != "" && cacheDir != nil {
			cacheDir(msg.CacheDir)
		}
		done = done || msg.Done
		if msg.Fatal != "" {
			fatal = msg.Fatal
		}
	}
	err = cmd.Wait()
	switch {
	case fatal != "":
		return fmt.Errorf("model download failed: %s", fatal)
	case err != nil:
		return fmt.Errorf("model download failed: %w", err)
	case !done:
		return errors.New("model download did not finish")
	}
	return nil
}
