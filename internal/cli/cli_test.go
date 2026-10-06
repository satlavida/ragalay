package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestPhase1Flow is plan1 Phase 1's exit criterion:
// ragalay init && ragalay folders add docs && ragalay status --json
func TestPhase1Flow(t *testing.T) {
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
