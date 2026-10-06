// Phase 0 spike: tursogo locking behaviour on Windows.
// Measures in-process multi-connection use and the cost of an
// open -> query -> close cycle (the pattern for short-lived connections).
package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	_ "turso.tech/database/tursogo"
)

func main() {
	path := os.Args[len(os.Args)-1]
	if len(os.Args) > 2 && os.Args[1] == "-cycle" {
		cycle(path, 20)
		return
	}
	// 1. One sql.DB, pool of connections, concurrent readers + a writer.
	db, err := sql.Open("turso", path)
	check(err)
	db.SetMaxOpenConns(4)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				var c int
				if err := db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&c); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			if _, err := db.Exec(`UPDATE chunks SET text = text WHERE id = 1`); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	n := 0
	for e := range errs {
		n++
		fmt.Println("  in-process pool error:", e)
	}
	fmt.Println("in-process pool (4 readers + writer) errors:", n)

	// 2. Two separate sql.DB handles in the same process.
	db2, err := sql.Open("turso", path)
	check(err)
	var c int
	fmt.Println("second sql.DB in same process:", db2.QueryRow(`SELECT count(*) FROM chunks`).Scan(&c), c)
	db2.Close()
	db.Close()

	// 3. Open/query/close cycle cost, and a child process cycling while we are closed.
	cycle(path, 20)
	exe, _ := os.Executable()
	child := exec.Command(exe, "-cycle", path)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	fmt.Println("child process cycle (parent closed):", child.Run())
}

func cycle(path string, n int) {
	var total time.Duration
	for i := 0; i < n; i++ {
		t := time.Now()
		db, err := sql.Open("turso", path)
		check(err)
		var c int
		if err := db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&c); err != nil {
			fmt.Println("  cycle error:", err)
			return
		}
		db.Close()
		total += time.Since(t)
	}
	fmt.Printf("open+count+close avg over %d: %v (pid %d)\n", n, total/time.Duration(n), os.Getpid())
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
