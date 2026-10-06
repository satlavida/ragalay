// Phase 0 spike: what does tursogo support on this machine?
//
// Checks F32_BLOB vectors, vector_distance_cos, ANN index + vector_top_k,
// FTS, brute-force search speed, and a reader process alongside a writer.
//
// Usage: go run ./spikes/turso <dbpath>
//        go run ./spikes/turso -reader <dbpath>   (internal, spawned by the writer)
package main

import (
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "turso.tech/database/tursogo"
)

const dim = 1024

func main() {
	reader := flag.Bool("reader", false, "run as reader process")
	n := flag.Int("n", 10000, "rows for the speed test")
	exp := flag.String("exp", "", "experimental features, e.g. index_method,multiprocess_wal")
	flag.Parse()
	path := flag.Arg(0)
	if path == "" {
		path = "spike.db"
	}
	dsn := path
	if *exp != "" {
		dsn = path + "?experimental=" + *exp
	}
	if *reader {
		runReader(dsn)
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}

	db, err := sql.Open("turso", dsn)
	must(err)
	defer db.Close()

	var version string
	try(db, "sqlite_version", func() error { return db.QueryRow("select sqlite_version()").Scan(&version) })
	fmt.Println("sqlite_version:", version)

	must2(db.Exec(`CREATE TABLE chunks (id INTEGER PRIMARY KEY, text TEXT, embedding F32_BLOB(1024))`))

	// Insert with vector32() from JSON text and with a raw little-endian blob.
	v1 := randVec()
	try(db, "insert via vector32(json)", func() error {
		_, err := db.Exec(`INSERT INTO chunks (id, text, embedding) VALUES (1, 'json path', vector32(?))`, toJSON(v1))
		return err
	})
	try(db, "insert via raw f32 blob", func() error {
		_, err := db.Exec(`INSERT INTO chunks (id, text, embedding) VALUES (2, 'blob path', ?)`, toBlob(v1))
		return err
	})
	try(db, "vector_distance_cos json vs blob (expect ~0)", func() error {
		var d float64
		err := db.QueryRow(`SELECT vector_distance_cos(a.embedding, b.embedding) FROM chunks a, chunks b WHERE a.id=1 AND b.id=2`).Scan(&d)
		fmt.Printf("    distance = %.6f\n", d)
		return err
	})
	try(db, "vector_extract roundtrip", func() error {
		var s string
		err := db.QueryRow(`SELECT substr(vector_extract(embedding), 1, 40) FROM chunks WHERE id=2`).Scan(&s)
		fmt.Printf("    %s...\n", s)
		return err
	})

	// Bulk insert for the speed test.
	start := time.Now()
	tx, err := db.Begin()
	must(err)
	stmt, err := tx.Prepare(`INSERT INTO chunks (id, text, embedding) VALUES (?, ?, ?)`)
	must(err)
	words := []string{"transformer", "attention", "encoder", "decoder", "vector", "search", "turso", "golang", "pdf", "markdown", "image", "recipe"}
	for i := 3; i < *n+3; i++ {
		text := fmt.Sprintf("%s %s %s chunk %d", words[i%len(words)], words[(i*7)%len(words)], words[(i*13)%len(words)], i)
		_, err := stmt.Exec(i, text, toBlob(randVec()))
		must(err)
	}
	must(stmt.Close())
	must(tx.Commit())
	fmt.Printf("bulk insert %d rows: %v\n", *n, time.Since(start))

	q := toBlob(randVec())
	try(db, "brute-force top-10 ORDER BY vector_distance_cos", func() error {
		t := time.Now()
		rows, err := db.Query(`SELECT id, vector_distance_cos(embedding, ?) AS d FROM chunks ORDER BY d LIMIT 10`, q)
		if err != nil {
			return err
		}
		cnt := 0
		for rows.Next() {
			cnt++
		}
		rows.Close()
		fmt.Printf("    %d rows in %v\n", cnt, time.Since(t))
		return rows.Err()
	})

	// ANN index: try libSQL syntax, then alternatives.
	annOK := false
	for _, ddl := range []string{
		`CREATE INDEX chunks_vec ON chunks(libsql_vector_idx(embedding))`,
		`CREATE INDEX chunks_vec ON chunks USING vector (embedding)`,
		`CREATE INDEX chunks_vec ON chunks USING toy_vector_sparse_ivf (embedding)`,
	} {
		ok := try(db, "ANN index: "+ddl, func() error { _, err := db.Exec(ddl); return err })
		if ok {
			annOK = true
			break
		}
	}
	if annOK {
		try(db, "vector_top_k('chunks_vec', q, 10)", func() error {
			t := time.Now()
			rows, err := db.Query(`SELECT id FROM vector_top_k('chunks_vec', ?, 10)`, q)
			if err != nil {
				return err
			}
			cnt := 0
			for rows.Next() {
				cnt++
			}
			rows.Close()
			fmt.Printf("    %d rows in %v\n", cnt, time.Since(t))
			return rows.Err()
		})
	}

	// Full-text search.
	for _, ddl := range []string{
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(text, content='chunks', content_rowid='id')`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(text)`,
		`CREATE INDEX chunks_fts_idx ON chunks USING fts (text)`,
	} {
		if try(db, "FTS: "+ddl, func() error { _, err := db.Exec(ddl); return err }) {
			break
		}
	}
	try(db, "FTS query: MATCH on fts5 table", func() error {
		db.Exec(`INSERT INTO chunks_fts(rowid, text) SELECT id, text FROM chunks`)
		var c int
		err := db.QueryRow(`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH 'attention'`).Scan(&c)
		fmt.Printf("    matches: %d\n", c)
		return err
	})
	try(db, "FTS query: bm25 ranking", func() error {
		rows, err := db.Query(`SELECT rowid, bm25(chunks_fts) FROM chunks_fts WHERE chunks_fts MATCH 'attention encoder' ORDER BY bm25(chunks_fts) LIMIT 3`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int
			var s float64
			rows.Scan(&id, &s)
			fmt.Printf("    id=%d bm25=%.3f\n", id, s)
		}
		rows.Close()
		return rows.Err()
	})
	try(db, "FTS query: fts_match/fts_score on USING fts index", func() error {
		rows, err := db.Query(`SELECT id, fts_score(text, 'attention encoder') AS s FROM chunks WHERE fts_match(text, 'attention encoder') ORDER BY s DESC LIMIT 3`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int
			var s float64
			rows.Scan(&id, &s)
			fmt.Printf("    id=%d score=%.3f\n", id, s)
		}
		rows.Close()
		return rows.Err()
	})

	// Writer + separate reader process.
	fmt.Println("cross-process: spawning reader while writer holds a write transaction")
	exe, _ := os.Executable()
	tx2, err := db.Begin()
	must(err)
	_, err = tx2.Exec(`INSERT INTO chunks (id, text, embedding) VALUES (?, 'uncommitted', ?)`, *n+100, toBlob(randVec()))
	must(err)
	cmd := exec.Command(exe, "-reader", "-exp", *exp, path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	runErr := cmd.Run()
	must(tx2.Commit())
	fmt.Println("  reader exit:", runErr)
	cmd = exec.Command(exe, "-reader", "-exp", *exp, path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	fmt.Println("  reader after commit, writer connection still open:", cmd.Run())
}

func runReader(path string) {
	db, err := sql.Open("turso", path)
	if err != nil {
		fmt.Println("  [reader] open:", err)
		os.Exit(1)
	}
	defer db.Close()
	var c int
	t := time.Now()
	if err := db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&c); err != nil {
		fmt.Println("  [reader] query:", err)
		os.Exit(2)
	}
	fmt.Printf("  [reader] saw %d rows in %v\n", c, time.Since(t))
}

func try(_ *sql.DB, label string, fn func() error) bool {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("PANIC %s: %v\n", label, r)
		}
	}()
	if err := fn(); err != nil {
		fmt.Printf("FAIL  %s: %v\n", label, err)
		return false
	}
	fmt.Printf("OK    %s\n", label)
	return true
}

func randVec() []float32 {
	v := make([]float32, dim)
	var s float64
	for i := range v {
		v[i] = float32(rand.NormFloat64())
		s += float64(v[i] * v[i])
	}
	n := float32(math.Sqrt(s))
	for i := range v {
		v[i] /= n
	}
	return v
}

func toBlob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func toJSON(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func must2(_ any, err error) { must(err) }
