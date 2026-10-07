package cli

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/store"
)

// serviceRoot makes a folder whose model is an OpenAI-compatible service.
func serviceRoot(t *testing.T, baseURL string) string {
	t.Helper()
	t.Setenv("RAGALAY_CACHE", t.TempDir()) // nothing installed: no Python, no downloads
	root := t.TempDir()
	if code, _, stderr := runCLI(t, "init", root); code != ExitOK {
		t.Fatalf("init: %s", stderr)
	}
	cfg, _ := config.Load(root)
	cfg.Embed, _ = cfg.Embed.UseProfile("openai", 8)
	cfg.Embed.OpenAI.BaseURL = baseURL
	cfg.Embed.OpenAI.Model = "test"
	if err := config.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "notes.md"), []byte("# Notes\n\nMulti-head attention and sourdough bread."), 0o644)
	return root
}

// A service on another computer gets documents only after consent, asked
// once per folder (plan2 S10).
func TestRemoteServiceNeedsConsent(t *testing.T) {
	root := serviceRoot(t, "http://embeddings.example.invalid/v1")
	code, out, stderr := runCLI(t, "scan", "--root", root)
	if code == ExitOK || !strings.Contains(out+stderr, "--yes") || !strings.Contains(out+stderr, "embeddings.example.invalid") {
		t.Fatalf("scan without consent: %d\n%s\n%s", code, out, stderr)
	}
	// With --yes the consent is recorded (the made-up host then fails).
	runCLI(t, "scan", "--root", root, "--yes")
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		if v, _ := store.Meta(ctx, db, consentKey("embeddings.example.invalid")); v == "" {
			t.Error("consent not recorded")
		}
		return nil
	})
}

// A local service indexes and searches with nothing installed.
func TestLocalServiceEndToEnd(t *testing.T) {
	var queries int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var data []map[string]any
		for i, s := range body.Input {
			if strings.Contains(s, "?") {
				queries++
			}
			// Bag of two words: attention, bread.
			v := []float32{0.01, 0.01, 0, 0, 0, 0, 0, 0}
			if strings.Contains(s, "attention") {
				v[0] = 1
			}
			if strings.Contains(s, "bread") {
				v[1] = 1
			}
			b := make([]byte, 32)
			for j, x := range v {
				binary.LittleEndian.PutUint32(b[4*j:], math.Float32bits(x))
			}
			data = append(data, map[string]any{"index": i, "embedding": base64.StdEncoding.EncodeToString(b)})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	root := serviceRoot(t, srv.URL+"/v1")
	if code, out, stderr := runCLI(t, "scan", "--root", root); code != ExitOK || !strings.Contains(out, "Indexed 1") {
		t.Fatalf("scan: %d\n%s\n%s", code, out, stderr)
	}
	code, out, stderr := runCLI(t, "search", "--root", root, "--mode", "vector", "--json", "what about attention?")
	if code != ExitOK || !strings.Contains(out, "notes.md") || queries != 1 {
		t.Fatalf("search: %d queries=%d\n%s\n%s", code, queries, out, stderr)
	}
	entries, _ := os.ReadDir(os.Getenv("RAGALAY_CACHE"))
	if len(entries) != 0 {
		t.Fatalf("a service profile must not install anything, found %v", entries)
	}
}
