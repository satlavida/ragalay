package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// CacheDir is the shared, per-user folder for the large downloads (uv,
// Python, torch, llama.cpp, models). Override with RAGALAY_CACHE.
func CacheDir() (string, error) {
	if d := os.Getenv("RAGALAY_CACHE"); d != "" {
		return filepath.Abs(d)
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ragalay"), nil
}

// State records what setup has installed on this machine. It lives in the
// cache dir, so every ragalay folder on the machine shares one setup.
type State struct {
	UV               string  `json:"uv,omitempty"` // uv binary path
	UVVersion        string  `json:"uv_version,omitempty"`
	Variant          string  `json:"variant,omitempty"`       // installed torch variant
	Python           string  `json:"python,omitempty"`        // venv python path
	PackagesHash     string  `json:"packages_hash,omitempty"` // pins the venv was built from
	Sidecar          string  `json:"sidecar,omitempty"`       // sidecar.py path
	IndexModel       string  `json:"index_model,omitempty"`   // "model@revision" downloaded
	LlamaVersion     string  `json:"llama_version,omitempty"`
	LlamaLib         string  `json:"llama_lib,omitempty"`   // folder with the llama.cpp library
	QueryModel       string  `json:"query_model,omitempty"` // GGUF path
	Device           string  `json:"device,omitempty"`      // device the self-test ran on
	DeviceName       string  `json:"device_name,omitempty"`
	LicenseAccepted  string  `json:"license_accepted_at,omitempty"`
	SelfTestParity   float64 `json:"self_test_parity,omitempty"`
	SelfTestPassedAt string  `json:"self_test_passed_at,omitempty"`
}

func statePath(cache string) string { return filepath.Join(cache, "state.json") }

// LoadState reads the machine state; a missing file is an empty state.
func LoadState(cache string) (State, error) {
	var s State
	b, err := os.ReadFile(statePath(cache))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Save writes the state atomically.
func (s State) Save(cache string) error {
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(cache) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(cache))
}

// Ready reports whether everything needed for indexing and search is
// installed and passed the self-test.
func (s State) Ready() bool {
	return s.SelfTestPassedAt != "" && fileExists(s.Python) && fileExists(s.Sidecar) &&
		fileExists(s.QueryModel) && s.LlamaLib != "" && s.LicenseAccepted != ""
}

// QueryReady reports whether search can embed queries (llama.cpp + GGUF).
func (s State) QueryReady() bool { return s.LlamaLib != "" && fileExists(s.QueryModel) }

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
