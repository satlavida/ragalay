package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/embed"
)

// Installed is a local profile's models in the shared cache.
type Installed struct {
	Profile string `json:"profile"`
	Title   string `json:"title"`
	Bytes   int64  `json:"bytes"`
	gguf    string
	model   string // Hugging Face repo of the indexing model, if cached
}

// hfHubCache is where the sidecar's Hugging Face downloads live (the same
// rules huggingface_hub uses).
func hfHubCache() string {
	if d := os.Getenv("HF_HUB_CACHE"); d != "" {
		return d
	}
	if d := os.Getenv("HF_HOME"); d != "" {
		return filepath.Join(d, "hub")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// hfCache asks huggingface_hub (through the sidecar script) for a cached
// model's size, deleting it when remove is set. The cache shares blobs
// between files and models, so it cannot be measured or deleted by walking
// one folder.
func hfCache(st State, model string, remove bool) (int64, error) {
	if !fileExists(st.Python) || !fileExists(st.Sidecar) {
		return 0, fmt.Errorf("the Python environment is not installed")
	}
	cmd := "cache-size"
	if remove {
		cmd = "cache-delete"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, st.Python, st.Sidecar, cmd, "--model", model).Output()
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", cmd, model, err)
	}
	var res struct {
		Bytes int64  `json:"bytes"`
		Fatal string `json:"fatal"`
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if err := json.Unmarshal(lines[len(lines)-1], &res); err != nil || res.Fatal != "" {
		return 0, fmt.Errorf("%s %s: %v %s", cmd, model, err, res.Fatal)
	}
	return res.Bytes, nil
}

// approxSize follows the cache's links when Python is not available.
func approxSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// ListInstalled reports the models each local profile has on disk.
// Models are shared by every folder on the computer, so nothing is removed
// automatically (plan2 S15).
func ListInstalled(cache string) []Installed {
	st, _ := LoadState(cache)
	EnsureSidecar(cache, &st) // an older script has no cache command
	var out []Installed
	for _, p := range embed.Profiles() {
		if !p.Local() {
			continue
		}
		in := Installed{Profile: p.Name, Title: p.Title}
		if q, ok := queryModels[p.Name]; ok {
			if fi, err := os.Stat(filepath.Join(cache, "models", q.File)); err == nil {
				in.gguf = filepath.Join(cache, "models", q.File)
				in.Bytes += fi.Size()
			}
		}
		dir := filepath.Join(hfHubCache(), "models--"+strings.ReplaceAll(p.IndexModel, "/", "--"))
		if _, err := os.Stat(dir); err == nil {
			in.model = p.IndexModel
			if n, err := hfCache(st, p.IndexModel, false); err == nil {
				in.Bytes += n
			} else {
				in.Bytes += approxSize(dir)
			}
		}
		if in.gguf != "" || in.model != "" {
			out = append(out, in)
		}
	}
	return out
}

// Remove deletes a profile's models and forgets that it was set up.
func Remove(cache, profile string) error {
	st, err := LoadState(cache)
	if err != nil {
		return err
	}
	for _, in := range ListInstalled(cache) {
		if in.Profile != profile {
			continue
		}
		if in.gguf != "" {
			if err := os.Remove(in.gguf); err != nil {
				return err
			}
		}
		if in.model != "" {
			if _, err := hfCache(st, in.model, true); err != nil {
				return fmt.Errorf("remove the indexing model: %w", err)
			}
		}
	}
	if _, ok := st.Profiles[profile]; ok {
		delete(st.Profiles, profile)
		return st.Save(cache)
	}
	return nil
}
