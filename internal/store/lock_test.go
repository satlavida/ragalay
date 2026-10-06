package store

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHelperHoldDB is not a real test: TestWithRetriesWhileLocked runs the
// test binary again with RAGALAY_HOLD_DB set, so this process holds the
// database open (the way an indexer does) for a while.
func TestHelperHoldDB(t *testing.T) {
	path := os.Getenv("RAGALAY_HOLD_DB")
	if path == "" {
		t.Skip("helper process only")
	}
	db, err := sql.Open("turso", path)
	if err != nil {
		os.Exit(2)
	}
	if err := db.Ping(); err != nil {
		os.Exit(3)
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(700 * time.Millisecond)
	db.Close()
	os.Exit(0)
}

func TestWithRetriesWhileLocked(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldDB$")
	cmd.Env = append(os.Environ(), "RAGALAY_HOLD_DB="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
		t.Fatalf("helper did not start: %q", line)
	}

	start := time.Now()
	err = With(ctx, path, func(db *sql.DB) error {
		_, err := ReadStats(ctx, db)
		return err
	})
	if err != nil {
		t.Fatalf("With should retry until the other process closes the db: %v", err)
	}
	t.Logf("read succeeded after %v", time.Since(start))
}

func TestWithGivesUpAfterLockWait(t *testing.T) {
	ctx := context.Background()
	path := newDB(t)
	old := LockWait
	LockWait = 100 * time.Millisecond
	defer func() { LockWait = old }()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldDB$")
	cmd.Env = append(os.Environ(), "RAGALAY_HOLD_DB="+path)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	bufio.NewReader(out).ReadString('\n')

	err := With(ctx, path, func(db *sql.DB) error { return nil })
	if err == nil {
		t.Skip("this platform does not lock the database across processes")
	}
	if err != ErrBusy {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}
