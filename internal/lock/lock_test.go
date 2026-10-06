package lock

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireIsExclusive(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if info, alive := Read(dir); !alive || info.PID != os.Getpid() || info.Command != "scan" {
		t.Fatalf("Read = %+v, %v", info, alive)
	}
	_, err = Acquire(dir, "scan --watch")
	var held *HeldError
	if !errors.As(err, &held) || held.Info.PID != os.Getpid() {
		t.Fatalf("second Acquire = %v, want HeldError", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, alive := Read(dir); alive {
		t.Fatal("released lock still reads as held")
	}
	l2, err := Acquire(dir, "scan")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l2.Release()
}

func TestStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	// A process that has exited: run a short-lived command and use its pid.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Info{PID: cmd.Process.Pid, Command: "crashed", Started: time.Now().Add(-time.Hour)})
	os.WriteFile(filepath.Join(dir, FileName), b, 0o644)

	if _, alive := Read(dir); alive {
		t.Fatal("dead process reads as alive")
	}
	l, err := Acquire(dir, "scan")
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	l.Release()
}

func TestGarbageLockFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte("not json"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(p, old, old)
	l, err := Acquire(dir, "scan")
	if err != nil {
		t.Fatalf("old garbage lock should be treated as stale: %v", err)
	}
	l.Release()
}
