package sidecar

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// TestHelperSidecar is a fake sidecar.py: the tests run the test binary as
// the "python" executable. It speaks the real protocol with fake vectors.
func TestHelperSidecar(t *testing.T) {
	mode := os.Getenv("RAGALAY_FAKE_SIDECAR")
	if mode == "" {
		t.Skip("helper process only")
	}
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	switch mode {
	case "fatal":
		send(map[string]any{"fatal": "ImportError: no torch"})
		os.Exit(1)
	case "hang":
		time.Sleep(time.Minute)
	}
	send(map[string]any{"ready": true, "model": "fake", "device": "cpu", "dim": 4})
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	for in.Scan() {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Inputs []embed.Input `json:"inputs"`
			} `json:"params"`
		}
		json.Unmarshal(in.Bytes(), &req)
		switch req.Method {
		case "embed_documents":
			var vs []string
			for i, it := range req.Params.Inputs {
				if it.Text == "crash" {
					os.Exit(3) // simulate the process dying mid-call
				}
				v := []float32{float32(i + 1), float32(len(it.Text)), 0, 1}
				vs = append(vs, base64.StdEncoding.EncodeToString(embed.Blob(v)))
			}
			send(map[string]any{"id": req.ID, "result": map[string]any{"vectors": vs}})
		case "slow":
			time.Sleep(time.Minute)
		case "shutdown":
			send(map[string]any{"id": req.ID, "result": map[string]any{}})
			os.Exit(0)
		default:
			send(map[string]any{"id": req.ID, "error": map[string]any{"message": "ValueError: unknown method " + req.Method}})
		}
	}
	os.Exit(0)
}

func fakeOpts(t *testing.T, mode string) Options {
	t.Helper()
	return Options{
		Python:       os.Args[0],
		ScriptPath:   "-test.run=^TestHelperSidecar$",
		Model:        "fake",
		Revision:     "r",
		Env:          []string{"RAGALAY_FAKE_SIDECAR=" + mode},
		StartTimeout: 10 * time.Second,
	}
}

func TestEmbedDocumentsRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, err := Start(ctx, fakeOpts(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Info().Dim != 4 || c.Info().Model != "fake" {
		t.Fatalf("info = %+v", c.Info())
	}
	vs, err := c.EmbedDocuments(ctx, []embed.Input{{Modality: embed.Text, Text: "abc"}, {Modality: embed.Text, Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0][0] != 1 || vs[1][1] != 5 {
		t.Fatalf("vectors = %v", vs)
	}
}

func TestErrorsAreReported(t *testing.T) {
	ctx := context.Background()
	c, err := Start(ctx, fakeOpts(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(ctx, "nope", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method nope") {
		t.Fatalf("got %v", err)
	}
}

func TestRestartsAfterCrash(t *testing.T) {
	ctx := context.Background()
	c, err := Start(ctx, fakeOpts(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The crash input kills the process; the client restarts it and retries,
	// which crashes again, so the call fails with ErrDied...
	_, err = c.EmbedDocuments(ctx, []embed.Input{{Modality: embed.Text, Text: "crash"}})
	if !errors.Is(err, ErrDied) {
		t.Fatalf("want ErrDied, got %v", err)
	}
	// ...but the client is usable again afterwards.
	if _, err := c.EmbedDocuments(ctx, []embed.Input{{Modality: embed.Text, Text: "ok"}}); err != nil {
		t.Fatalf("client not usable after crash: %v", err)
	}
}

func TestCancelKillsCall(t *testing.T) {
	c, err := Start(context.Background(), fakeOpts(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "slow", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if _, err := c.EmbedDocuments(context.Background(), []embed.Input{{Modality: embed.Text, Text: "x"}}); err != nil {
		t.Fatalf("client not usable after cancel: %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	_, err := Start(context.Background(), fakeOpts(t, "fatal"))
	if err == nil || !strings.Contains(err.Error(), "no torch") {
		t.Fatalf("fatal: got %v", err)
	}
	o := fakeOpts(t, "hang")
	o.StartTimeout = 300 * time.Millisecond
	if _, err := Start(context.Background(), o); err == nil || !strings.Contains(err.Error(), "did not load") {
		t.Fatalf("hang: got %v", err)
	}
	o = fakeOpts(t, "ok")
	o.Python = "definitely-not-a-python-binary"
	if _, err := Start(context.Background(), o); err == nil {
		t.Fatal("missing python should fail")
	}
}

func TestScriptIsEmbedded(t *testing.T) {
	if !strings.Contains(string(Script), "def serve(") {
		t.Fatal("sidecar.py not embedded")
	}
}
