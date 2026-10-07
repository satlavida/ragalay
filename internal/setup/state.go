package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/satlavida/ragalay/internal/embed"
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
	UV           string `json:"uv,omitempty"` // uv binary path
	UVVersion    string `json:"uv_version,omitempty"`
	Variant      string `json:"variant,omitempty"`       // installed torch variant
	Python       string `json:"python,omitempty"`        // venv python path
	PackagesHash string `json:"packages_hash,omitempty"` // pins the venv was built from
	Sidecar      string `json:"sidecar,omitempty"`       // sidecar.py path
	LlamaVersion string `json:"llama_version,omitempty"`
	LlamaLib     string `json:"llama_lib,omitempty"` // folder with the llama.cpp library
	Device       string `json:"device,omitempty"`    // device the last self-test ran on
	DeviceName   string `json:"device_name,omitempty"`
	// Profiles holds the models installed per embedding profile (plan2).
	Profiles map[string]ProfileState `json:"profiles,omitempty"`

	// Plan 1 kept one model (Jina). LoadState moves these into Profiles.
	IndexModel       string  `json:"index_model,omitempty"`
	QueryModel       string  `json:"query_model,omitempty"`
	LicenseAccepted  string  `json:"license_accepted_at,omitempty"`
	SelfTestParity   float64 `json:"self_test_parity,omitempty"`
	SelfTestPassedAt string  `json:"self_test_passed_at,omitempty"`
}

// ProfileState is what setup installed for one local profile.
type ProfileState struct {
	IndexModel       string  `json:"index_model,omitempty"` // "model@revision" downloaded
	QueryModel       string  `json:"query_model,omitempty"` // GGUF path
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
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	s.migrate()
	return s, nil
}

// migrate moves Plan 1's single (Jina) model into Profiles.
func (s *State) migrate() {
	if s.IndexModel == "" && s.QueryModel == "" && s.LicenseAccepted == "" {
		return
	}
	if _, ok := s.Profiles[embed.JinaV5]; !ok {
		s.SetProfile(embed.JinaV5, ProfileState{
			IndexModel: s.IndexModel, QueryModel: s.QueryModel, LicenseAccepted: s.LicenseAccepted,
			SelfTestParity: s.SelfTestParity, SelfTestPassedAt: s.SelfTestPassedAt,
		})
	}
	s.IndexModel, s.QueryModel, s.LicenseAccepted, s.SelfTestParity, s.SelfTestPassedAt = "", "", "", 0, ""
}

// Profile returns what is installed for a profile.
func (s State) Profile(name string) ProfileState { return s.Profiles[name] }

// SetProfile records what is installed for a profile.
func (s *State) SetProfile(name string, p ProfileState) {
	if s.Profiles == nil {
		s.Profiles = map[string]ProfileState{}
	}
	s.Profiles[name] = p
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

// Ready reports whether everything a profile needs for indexing and search
// is installed and passed the self-test. A profile that only talks to a
// service (openai) needs nothing installed.
func (s State) Ready(profile string) bool {
	p, ok := embed.Lookup(profile)
	if !ok {
		return false
	}
	if !p.Local() {
		return true
	}
	ps := s.Profiles[profile]
	return ps.SelfTestPassedAt != "" && ps.IndexModel == p.IndexModel+"@"+p.IndexRevision &&
		(!p.NonCommercial || ps.LicenseAccepted != "") &&
		fileExists(s.Python) && fileExists(s.Sidecar) && s.QueryReady(profile)
}

// QueryReady reports whether search can embed queries for a local profile
// (llama.cpp at the pinned version + the profile's GGUF).
func (s State) QueryReady(profile string) bool {
	return s.LlamaLib != "" && s.LlamaVersion == llamaVersion && fileExists(s.Profiles[profile].QueryModel)
}

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
