// Package setup installs and verifies everything a local embedding profile
// needs outside the binary: uv, a Python venv with the right torch build,
// the profile's indexing model, llama.cpp and the profile's query model. It
// is machine-wide (one setup in the cache dir serves every ragalay folder)
// and idempotent. Profiles are installed separately (plan2 §5.5).
package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/satlavida/ragalay/internal/download"
	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/llama"
	"github.com/satlavida/ragalay/internal/sidecar"
)

// MinParity is the lowest acceptable cosine between the indexing model's and
// llama.cpp's embeddings of the same query (measured minimums: 0.9996 Jina,
// plan1 §3.1; 0.9998 Gemma, plan2 Phase 0).
const MinParity = 0.99

// Reporter receives progress. All methods may be called from one goroutine
// at a time.
type Reporter interface {
	Step(n, total int, title string)
	Progress(done, total int64)
	Note(msg string)
}

type nopReporter struct{}

func (nopReporter) Step(int, int, string) {}
func (nopReporter) Progress(int64, int64) {}
func (nopReporter) Note(string)           {}

// Options configure Run.
type Options struct {
	Device  string // torch variant override; "" or "auto" detects
	Profile string // local embedding profile (embed.Gemma2, embed.JinaV5)
	MaxSide int
	Log     io.Writer // command output and sidecar stderr
	Report  Reporter
}

// profile resolves opts.Profile to a local profile and its search model.
func (o Options) profile() (embed.Profile, QueryModel, error) {
	p, ok := embed.Lookup(o.Profile)
	if !ok || !p.Local() {
		return p, QueryModel{}, fmt.Errorf("%q is not a model ragalay installs", o.Profile)
	}
	q, ok := queryModels[p.Name]
	if !ok {
		return p, q, fmt.Errorf("no search model pinned for %s", p.Name)
	}
	return p, q, nil
}

// Item is one pending download, for the size prompt.
type Item struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Plan says what Run would do on this machine.
type Plan struct {
	Detection Detection `json:"detection"`
	Downloads []Item    `json:"downloads"`
	Total     int64     `json:"total_bytes"`
}

// Prepare inspects the cache and returns what still needs downloading.
func Prepare(ctx context.Context, cache string, opts Options) (Plan, error) {
	st, err := LoadState(cache)
	if err != nil {
		return Plan{}, err
	}
	prof, qm, err := opts.profile()
	if err != nil {
		return Plan{}, err
	}
	det := Detect(ctx, opts.Device)
	v, ok := variants[det.Variant]
	if !ok {
		return Plan{}, fmt.Errorf("unknown device %q (choose one of cpu, cuda, mps, rocm-gfx1201, rocm-gfx1200)", det.Variant)
	}
	p := Plan{Detection: det}
	add := func(name string, size int64) {
		p.Downloads = append(p.Downloads, Item{name, size})
		p.Total += size
	}
	if st.UVVersion != uvVersion || !fileExists(st.UV) {
		add("uv (Python manager)", uvAssets[platform()].Size)
	}
	switch {
	case st.Variant != v.Name || !fileExists(st.Python):
		add("Python 3.11 + PyTorch ("+v.Name+") + libraries", 60_000_000+v.Size+150_000_000)
	case st.PackagesHash != packagesHash(v):
		add("Python library updates", 20_000_000)
	}
	if st.Profile(prof.Name).IndexModel != prof.IndexModel+"@"+prof.IndexRevision {
		add("indexing model ("+prof.Title+")", indexModelSizes[prof.Name])
	}
	if st.LlamaVersion != llamaVersion || st.LlamaLib == "" {
		add("llama.cpp (search)", llamaAssets[platform()].Size)
	}
	if !fileExists(filepath.Join(cache, "models", qm.File)) {
		add("search model ("+prof.Title+")", qm.Size)
	}
	return p, nil
}

// Run performs (or completes) setup and returns the new state.
func Run(ctx context.Context, cache string, opts Options) (State, error) {
	if opts.Report == nil {
		opts.Report = nopReporter{}
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if _, ok := uvAssets[platform()]; !ok {
		return State{}, fmt.Errorf("ragalay does not support %s yet", platform())
	}
	st, err := LoadState(cache)
	if err != nil {
		return st, err
	}
	prof, qm, err := opts.profile()
	if err != nil {
		return st, err
	}
	r := opts.Report
	const steps = 7
	save := func() error { return st.Save(cache) }

	r.Step(1, steps, "Python manager (uv)")
	if err := ensureUV(ctx, cache, &st, r); err != nil {
		return st, err
	}
	if err := save(); err != nil {
		return st, err
	}

	r.Step(2, steps, "Python and PyTorch")
	det := Detect(ctx, opts.Device)
	r.Note(det.Reason)
	if err := ensureVenv(ctx, cache, &st, det.Variant, opts.Log, r); err != nil {
		return st, err
	}
	if err := save(); err != nil {
		return st, err
	}

	r.Step(3, steps, "Indexing helper")
	if err := EnsureSidecar(cache, &st); err != nil {
		return st, err
	}

	r.Step(4, steps, fmt.Sprintf("Indexing model (%s, about %s)", prof.Title, humanSize(indexModelSizes[prof.Name])))
	if err := ensureIndexModel(ctx, &st, prof, opts, r); err != nil {
		return st, err
	}
	if err := save(); err != nil {
		return st, err
	}

	r.Step(5, steps, "llama.cpp (for search)")
	if err := ensureLlama(ctx, cache, &st, r); err != nil {
		return st, err
	}
	if err := save(); err != nil {
		return st, err
	}

	r.Step(6, steps, "Search model")
	gguf := filepath.Join(cache, "models", qm.File)
	if err := download.File(ctx, qm.URL, gguf, qm.SHA256, r.Progress); err != nil {
		return st, err
	}
	ps := st.Profile(prof.Name)
	ps.QueryModel = gguf
	st.SetProfile(prof.Name, ps)
	if err := save(); err != nil {
		return st, err
	}

	r.Step(7, steps, "Self-test")
	if err := selfTest(ctx, &st, prof, opts, r); err != nil {
		return st, err
	}
	return st, save()
}

// EnsureSidecar writes the embedded sidecar script to the cache if it
// changed, so a ragalay update never runs an old script.
func EnsureSidecar(cache string, st *State) error {
	st.Sidecar = filepath.Join(cache, "py", "sidecar.py")
	return writeIfChanged(st.Sidecar, sidecar.Script)
}

// AcceptLicense records that the user accepted a profile's license.
func (s *State) AcceptLicense(profile string) {
	ps := s.Profile(profile)
	if ps.LicenseAccepted == "" {
		ps.LicenseAccepted = time.Now().UTC().Format(time.RFC3339)
	}
	s.SetProfile(profile, ps)
}

func humanSize(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	}
	return fmt.Sprintf("%d MB", n/1_000_000)
}

func ensureUV(ctx context.Context, cache string, st *State, r Reporter) error {
	if st.UVVersion == uvVersion && fileExists(st.UV) {
		return nil
	}
	a := uvAssets[platform()]
	archive := filepath.Join(cache, "downloads", filepath.Base(a.URL))
	if err := download.File(ctx, a.URL, archive, a.SHA256, r.Progress); err != nil {
		return err
	}
	dir := filepath.Join(cache, "bin", "uv-"+uvVersion)
	os.RemoveAll(dir)
	if err := download.Extract(archive, dir, ""); err != nil {
		return err
	}
	uv, err := findFile(dir, exe("uv"))
	if err != nil {
		return err
	}
	os.Chmod(uv, 0o755)
	os.Remove(archive)
	st.UV, st.UVVersion = uv, uvVersion
	return nil
}

func packagesHash(v Variant) string {
	h := sha256.New()
	fmt.Fprintln(h, pythonVersion, v.Name, v.Index, strings.Join(v.Packages, " "), strings.Join(pythonPackages, " "))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func venvPython(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}

func ensureVenv(ctx context.Context, cache string, st *State, variant string, log io.Writer, r Reporter) error {
	v, ok := variants[variant]
	if !ok {
		return fmt.Errorf("unknown device %q", variant)
	}
	if st.Variant == v.Name && st.PackagesHash == packagesHash(v) && fileExists(st.Python) {
		return nil
	}
	// Same torch build, newer library pins (e.g. transformers 5.19 for
	// Gemma): update in place instead of downloading PyTorch again.
	if st.Variant == v.Name && fileExists(st.Python) {
		if err := installPackages(ctx, cache, st, log, r); err == nil {
			st.PackagesHash = packagesHash(v)
			return nil
		}
		r.Note("Updating the Python libraries failed; rebuilding the Python environment.")
	}
	if err := installVenv(ctx, cache, st, v, log, r); err != nil {
		if v.Name == "cpu" {
			return err
		}
		r.Note(fmt.Sprintf("Installing PyTorch for %s failed (%v); using the CPU instead.", v.Name, err))
		return installVenv(ctx, cache, st, variants["cpu"], log, r)
	}
	if v.Name == "cpu" {
		return nil
	}
	accel, err := checkAccelerator(ctx, st.Python)
	if err != nil || !accel.Available {
		r.Note(fmt.Sprintf("PyTorch (%s) installed but cannot use the GPU; using the CPU instead. %v", v.Name, err))
		return installVenv(ctx, cache, st, variants["cpu"], log, r)
	}
	r.Note("GPU ready: " + accel.Name)
	return nil
}

func installVenv(ctx context.Context, cache string, st *State, v Variant, log io.Writer, r Reporter) error {
	dir := filepath.Join(cache, "py", v.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove old Python environment: %w", err)
	}
	env := uvEnv(cache)
	r.Note("Creating Python " + pythonVersion + " environment")
	if err := runCmd(ctx, log, env, st.UV, "venv", "--python", pythonVersion, "--python-preference", "only-managed", dir); err != nil {
		return err
	}
	py := venvPython(dir)
	r.Note(fmt.Sprintf("Installing PyTorch (%s, about %d MB, this takes a while)", v.Name, v.Size/1_000_000))
	args := []string{"pip", "install", "--python", py}
	if v.Index != "" {
		args = append(args, "--index-url", v.Index, "--extra-index-url", "https://pypi.org/simple", "--index-strategy", "unsafe-best-match")
	}
	if err := runCmd(ctx, log, env, st.UV, append(args, v.Packages...)...); err != nil {
		return err
	}
	st.Python = py
	if err := installPackages(ctx, cache, st, log, r); err != nil {
		return err
	}
	st.Variant, st.PackagesHash = v.Name, packagesHash(v)
	return nil
}

// installPackages installs the pinned model libraries into st.Python.
func installPackages(ctx context.Context, cache string, st *State, log io.Writer, r Reporter) error {
	r.Note("Installing model libraries")
	return runCmd(ctx, log, uvEnv(cache), st.UV, append([]string{"pip", "install", "--python", st.Python}, pythonPackages...)...)
}

func uvEnv(cache string) []string {
	env := []string{
		"UV_PYTHON_INSTALL_DIR=" + filepath.Join(cache, "python"),
		"UV_NO_CONFIG=1",
	}
	// Keep uv's package cache with ours unless the user already has one.
	if os.Getenv("UV_CACHE_DIR") == "" {
		env = append(env, "UV_CACHE_DIR="+filepath.Join(cache, "uv-cache"))
	}
	return env
}

type accelerator struct {
	Available bool   `json:"available"`
	Name      string `json:"name"`
}

func checkAccelerator(ctx context.Context, py string) (accelerator, error) {
	const script = `import json, torch
if torch.cuda.is_available():
    print(json.dumps({"available": True, "name": torch.cuda.get_device_name(0)}))
elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
    print(json.dumps({"available": True, "name": "Apple MPS"}))
else:
    print(json.dumps({"available": False, "name": ""}))`
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, py, "-c", script).Output()
	if err != nil {
		return accelerator{}, err
	}
	var a accelerator
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	err = json.Unmarshal([]byte(lines[len(lines)-1]), &a)
	return a, err
}

func ensureIndexModel(ctx context.Context, st *State, prof embed.Profile, opts Options, r Reporter) error {
	want := prof.IndexModel + "@" + prof.IndexRevision
	ps := st.Profile(prof.Name)
	if ps.IndexModel == want {
		return nil
	}
	size := indexModelSizes[prof.Name]
	modelDir := "models--" + strings.ReplaceAll(prof.IndexModel, "/", "--")
	var watched string
	stop := make(chan struct{})
	defer close(stop)
	dirs := make(chan string, 1)
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case d := <-dirs:
				watched = filepath.Join(d, modelDir)
			case <-t.C:
				if watched != "" {
					r.Progress(dirSize(watched), size)
				}
			}
		}
	}()
	err := sidecar.Download(ctx, sidecar.Options{
		Python: st.Python, ScriptPath: st.Sidecar, Model: prof.IndexModel, Revision: prof.IndexRevision, Log: opts.Log,
	}, func(d string) { dirs <- d })
	if err != nil {
		return err
	}
	ps.IndexModel = want
	st.SetProfile(prof.Name, ps)
	return nil
}

func ensureLlama(ctx context.Context, cache string, st *State, r Reporter) error {
	if st.LlamaVersion == llamaVersion && st.LlamaLib != "" && fileExists(filepath.Join(st.LlamaLib, llamaLibName())) {
		return nil
	}
	a := llamaAssets[platform()]
	archive := filepath.Join(cache, "downloads", filepath.Base(a.URL))
	if err := download.File(ctx, a.URL, archive, a.SHA256, r.Progress); err != nil {
		return err
	}
	dir := filepath.Join(cache, "llama.cpp", llamaVersion+"-cpu")
	os.RemoveAll(dir)
	if err := download.Extract(archive, dir, ""); err != nil {
		return err
	}
	lib, err := findFile(dir, llamaLibName())
	if err != nil {
		return err
	}
	os.Remove(archive)
	st.LlamaLib, st.LlamaVersion = filepath.Dir(lib), llamaVersion
	return nil
}

func llamaLibName() string {
	switch runtime.GOOS {
	case "windows":
		return "llama.dll"
	case "darwin":
		return "libllama.dylib"
	}
	return "libllama.so"
}

// selfTest embeds a document, an image and a query with the indexing
// sidecar, embeds the same query with llama.cpp, and checks they agree (G2).
func selfTest(ctx context.Context, st *State, prof embed.Profile, opts Options, r Reporter) error {
	img, err := testImage()
	if err != nil {
		return err
	}
	defer os.Remove(img)

	r.Note("Loading the indexing model")
	start := time.Now()
	sc, err := sidecar.Start(ctx, sidecar.Options{
		Python: st.Python, ScriptPath: st.Sidecar, Model: prof.IndexModel, Revision: prof.IndexRevision,
		MaxSide: opts.MaxSide, Log: opts.Log,
	})
	if err != nil {
		return err
	}
	defer sc.Close()
	info := sc.Info()
	r.Note(fmt.Sprintf("Indexing model loaded on %s (%s) in %.1fs", info.DeviceName, info.DType, time.Since(start).Seconds()))

	start = time.Now()
	docs, err := sc.EmbedDocuments(ctx, []embed.Input{
		{Modality: embed.Text, Text: "ragalay indexes Markdown, PDF and image files for local search."},
		{Modality: embed.Image, Path: img, Text: "a test pattern"},
	})
	if err != nil {
		return fmt.Errorf("self-test (indexing): %w", err)
	}
	if len(docs) != 2 || len(docs[0]) != info.Dim {
		return fmt.Errorf("self-test: unexpected document vectors (%d, dim %d)", len(docs), info.Dim)
	}
	r.Note(fmt.Sprintf("Embedded a text and an image in %.2fs", time.Since(start).Seconds()))

	const q = "how do I search my documents locally"
	indexQ, err := sc.EmbedQuery(ctx, embed.Input{Modality: embed.Text, Text: q})
	if err != nil {
		return fmt.Errorf("self-test (indexing model query): %w", err)
	}

	start = time.Now()
	ps := st.Profile(prof.Name)
	lq, err := llama.Open(st.LlamaLib, ps.QueryModel, llama.OptionsFor(prof))
	if err != nil {
		return fmt.Errorf("self-test (search model): %w", err)
	}
	defer lq.Close()
	llamaQ, err := lq.EmbedQuery(ctx, q)
	if err != nil {
		return fmt.Errorf("self-test (search query): %w", err)
	}
	r.Note(fmt.Sprintf("Search model loaded and embedded a query in %.2fs", time.Since(start).Seconds()))

	a, err := embed.Fit(indexQ, info.Dim)
	if err != nil {
		return err
	}
	b, err := embed.Fit(llamaQ, info.Dim)
	if err != nil {
		return err
	}
	parity := embed.Cosine(a, b)
	r.Note(fmt.Sprintf("Search and indexing models agree: %.4f (need %.2f)", parity, MinParity))
	if parity < MinParity {
		return fmt.Errorf("self-test: search and indexing models disagree (cosine %.4f < %.2f)", parity, MinParity)
	}
	st.Device, st.DeviceName = info.Device, info.DeviceName
	ps.SelfTestParity, ps.SelfTestPassedAt = parity, time.Now().UTC().Format(time.RFC3339)
	st.SetProfile(prof.Name, ps)
	return nil
}

func testImage() (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 2), uint8(y * 4), 128, 255})
		}
	}
	f, err := os.CreateTemp("", "ragalay-selftest-*.png")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return f.Name(), png.Encode(f, img)
}

// runCmd runs a command, copying its output to log. On failure the error
// includes the last lines of output.
func runCmd(ctx context.Context, log io.Writer, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var tail tailBuffer
	cmd.Stdout = io.MultiWriter(log, &tail)
	cmd.Stderr = io.MultiWriter(log, &tail)
	fmt.Fprintf(log, "\n$ %s %s\n", filepath.Base(name), strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w\n%s", filepath.Base(name), args[0], err, tail.String())
	}
	return nil
}

// tailBuffer keeps the last ~4 KB written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func findFile(dir, name string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in %s", name, dir)
	}
	return found, nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
