package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satlavida/ragalay/internal/lock"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errb, func() bool { return false })
	return code, out.String(), errb.String()
}

// TestPhase1Flow is plan1 Phase 1's exit criterion:
// ragalay init && ragalay folders add docs && ragalay status --json
func TestPhase1Flow(t *testing.T) {
	t.Setenv("RAGALAY_CACHE", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "docs", "sub"), 0o755)
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	t.Chdir(root)

	if code, _, stderr := runCLI(t, "status"); code != ExitNotInitialized {
		t.Fatalf("status before init: code %d, want %d (%s)", code, ExitNotInitialized, stderr)
	}

	code, out, stderr := runCLI(t, "init")
	if code != ExitOK || !strings.Contains(out, "Set up ragalay") {
		t.Fatalf("init: %d %q %q", code, out, stderr)
	}
	if code, out, _ = runCLI(t, "init"); code != ExitOK || !strings.Contains(out, "already set up") {
		t.Fatalf("second init: %d %q", code, out)
	}

	if code, out, stderr = runCLI(t, "folders", "add", "docs"); code != ExitOK || !strings.Contains(out, "added docs") {
		t.Fatalf("folders add: %d %q %q", code, out, stderr)
	}
	if code, out, _ = runCLI(t, "folders", "add", "docs/sub"); code != ExitOK || !strings.Contains(out, "covered by docs") {
		t.Fatalf("folders add child: %d %q", code, out)
	}
	if code, _, stderr = runCLI(t, "folders", "add", t.TempDir()); code != ExitError || !strings.Contains(stderr, "outside") {
		t.Fatalf("folders add outside: %d %q", code, stderr)
	}

	// From a subfolder, the root is found by walking up and paths resolve
	// against the current folder.
	t.Chdir(filepath.Join(root, "docs"))
	if code, out, stderr = runCLI(t, "folders", "add", "../notes"); code != ExitOK || !strings.Contains(out, "added notes") {
		t.Fatalf("folders add from subdir: %d %q %q", code, out, stderr)
	}

	code, out, stderr = runCLI(t, "status", "--json")
	if code != ExitOK {
		t.Fatalf("status --json: %d %q", code, stderr)
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if rep.WholeDir || len(rep.Folders) != 2 || rep.Folders[0].Path != "docs" || rep.Folders[1].Path != "notes" ||
		!rep.Folders[0].Exists || rep.Index.SchemaVersion != 1 || rep.Index.EmbedDim != 1024 ||
		rep.SetupComplete || len(rep.ConfigErrors) != 0 {
		t.Fatalf("unexpected status: %+v", rep)
	}

	if code, out, _ = runCLI(t, "folders", "remove", "../notes"); code != ExitOK || !strings.Contains(out, "removed notes") {
		t.Fatalf("folders remove: %d %q", code, out)
	}
	if code, _, stderr = runCLI(t, "folders", "remove", "nope"); code != ExitError {
		t.Fatalf("removing an unlisted folder should fail: %d %q", code, stderr)
	}
	code, out, _ = runCLI(t, "folders", "list", "--json")
	var list struct {
		WholeDirectory bool         `json:"whole_directory"`
		Folders        []FolderInfo `json:"folders"`
	}
	if code != ExitOK || json.Unmarshal([]byte(out), &list) != nil || len(list.Folders) != 1 || list.Folders[0].Path != "docs" {
		t.Fatalf("folders list --json: %d %s", code, out)
	}
}

func TestRootFlagAndConfigErrors(t *testing.T) {
	t.Setenv("RAGALAY_CACHE", t.TempDir())
	root := t.TempDir()
	t.Chdir(t.TempDir()) // somewhere unrelated
	if code, _, stderr := runCLI(t, "init", root); code != ExitOK {
		t.Fatalf("init <dir>: %d %s", code, stderr)
	}
	cfg := filepath.Join(root, ".ragalay", "config.toml")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(data), "dim = 1024", "dim = 1000", 1)), 0o644)

	code, out, stderr := runCLI(t, "status", "--json", "--root", root)
	if code != ExitOK {
		t.Fatalf("status --root: %d %s", code, stderr)
	}
	var rep StatusReport
	json.Unmarshal([]byte(out), &rep)
	if len(rep.ConfigErrors) != 1 || !strings.Contains(rep.ConfigErrors[0], "embed.dim") {
		t.Fatalf("config errors not reported: %+v", rep.ConfigErrors)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runCLI(t, "version", "--json")
	var v map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &v) != nil || v["version"] != "dev" {
		t.Fatalf("version --json: %d %s", code, out)
	}
}

// TestPhase3Flow is plan1 Phase 3's exit criterion: add/edit/move/delete/pair
// files show correctly in status --json, and a second indexer is refused.
func TestPhase3Flow(t *testing.T) {
	t.Setenv("RAGALAY_CACHE", t.TempDir())
	root := t.TempDir()
	t.Chdir(root)
	put := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	scanJSON := func() map[string]any {
		t.Helper()
		code, out, stderr := runCLI(t, "scan", "--json")
		if code != ExitOK {
			t.Fatalf("scan: %d %s", code, stderr)
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("scan --json: %v\n%s", err, out)
		}
		return rep
	}
	status := func() StatusReport {
		t.Helper()
		_, out, _ := runCLI(t, "status", "--json")
		var rep StatusReport
		json.Unmarshal([]byte(out), &rep)
		return rep
	}

	runCLI(t, "init")
	put("md/report.md", "# Report transcription")
	put("pdf/report.pdf", "%PDF-report")
	put("photos/cat.png", "png")
	if rep := scanJSON(); len(rep["new"].([]any)) != 3 || len(rep["pairs"].([]any)) != 1 {
		t.Fatalf("first scan: %v", rep)
	}
	if s := status(); s.Index.Documents != 3 || s.Index.ByStatus["pending"] != 3 || s.Queued != 3 || s.Pairs != 1 || s.Indexing != nil {
		t.Fatalf("status after first scan: %+v", s)
	}

	put("md/report.md", "# Report transcription, edited")
	os.Rename(filepath.Join(root, "photos", "cat.png"), filepath.Join(root, "photos", "kitty.png"))
	os.Remove(filepath.Join(root, "pdf", "report.pdf"))
	rep := scanJSON()
	if len(rep["changed"].([]any)) != 1 || len(rep["moved"].([]any)) != 1 || len(rep["deleted"].([]any)) != 1 {
		t.Fatalf("second scan: %v", rep)
	}
	if s := status(); s.Index.Documents != 2 || s.Pairs != 0 {
		t.Fatalf("status after second scan: %+v", s)
	}
	code, out, _ := runCLI(t, "docs", "--json", "--kind", "image")
	var docs []DocInfo
	if code != ExitOK || json.Unmarshal([]byte(out), &docs) != nil || len(docs) != 1 || docs[0].Path != "photos/kitty.png" {
		t.Fatalf("docs --kind image: %s", out)
	}

	// A second indexer is refused while the lock is held.
	l, err := lock.Acquire(filepath.Join(root, ".ragalay"), "scan --watch")
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "scan")
	if code != ExitLocked || !strings.Contains(stderr, "scan --watch") {
		t.Fatalf("second indexer: code %d, %s", code, stderr)
	}
	if s := status(); s.Indexing == nil || s.Indexing.Command != "scan --watch" {
		t.Fatalf("status must show the running indexer: %+v", s.Indexing)
	}
	l.Release()
	if code, _, stderr := runCLI(t, "scan"); code != ExitOK {
		t.Fatalf("scan after release: %d %s", code, stderr)
	}
}

func TestSetupNeedsLicenseWithoutTerminal(t *testing.T) {
	t.Setenv("RAGALAY_CACHE", t.TempDir())
	root := t.TempDir()
	if code, _, stderr := runCLI(t, "init", root); code != ExitOK {
		t.Fatalf("init: %s", stderr)
	}
	code, out, stderr := runCLI(t, "setup", "--root", root, "--device", "cpu")
	if code != ExitSetupIncomplete || !strings.Contains(out, "CC BY-NC 4.0") || !strings.Contains(stderr, "--accept-license") {
		t.Fatalf("setup without terminal: code %d\n%s\n%s", code, out, stderr)
	}
	// With the license accepted but no --yes, it still refuses to download silently.
	code, out, stderr = runCLI(t, "setup", "--root", root, "--device", "cpu", "--accept-license")
	if code != ExitSetupIncomplete || !strings.Contains(out, "To download:") || !strings.Contains(stderr, "--yes") {
		t.Fatalf("setup without --yes: code %d\n%s\n%s", code, out, stderr)
	}
}
