// Phase 0 spike: BM25 stored in Turso tables, scored in Go.
//
// Indexes ~N chunks of real text (PDF text via go-pdfium, repeated with
// variations to reach N), then times keyword queries.
//
// Usage: go run ./spikes/bm25 <db> <pdf>...
package main

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	_ "turso.tech/database/tursogo"
)

const target = 20000

func main() {
	dbPath := os.Args[1]
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(dbPath + s)
	}
	base := chunksFromPDFs(os.Args[2:])
	fmt.Printf("base chunks from PDFs: %d\n", len(base))

	db, err := sql.Open("turso", dbPath)
	check(err)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE chunks (id INTEGER PRIMARY KEY, text TEXT, len INTEGER)`,
		`CREATE TABLE terms (id INTEGER PRIMARY KEY, term TEXT UNIQUE, df INTEGER)`,
		`CREATE TABLE postings (term_id INTEGER, chunk_id INTEGER, tf INTEGER, PRIMARY KEY (term_id, chunk_id))`,
	} {
		_, err := db.Exec(ddl)
		check(err)
	}

	t := time.Now()
	termID := map[string]int64{}
	df := map[int64]int{}
	tx, err := db.Begin()
	check(err)
	insChunk, _ := tx.Prepare(`INSERT INTO chunks (id, text, len) VALUES (?, ?, ?)`)
	insPost, _ := tx.Prepare(`INSERT INTO postings (term_id, chunk_id, tf) VALUES (?, ?, ?)`)
	for i := 0; i < target; i++ {
		text := base[i%len(base)]
		if i >= len(base) {
			text = fmt.Sprintf("%s variant%d", text, i/len(base))
		}
		toks := tokenize(text)
		_, err := insChunk.Exec(i+1, text, len(toks))
		check(err)
		tf := map[string]int{}
		for _, w := range toks {
			tf[w]++
		}
		for w, c := range tf {
			id, ok := termID[w]
			if !ok {
				id = int64(len(termID) + 1)
				termID[w] = id
			}
			df[id]++
			_, err := insPost.Exec(id, i+1, c)
			check(err)
		}
	}
	insTerm, _ := tx.Prepare(`INSERT INTO terms (id, term, df) VALUES (?, ?, ?)`)
	for w, id := range termID {
		_, err := insTerm.Exec(id, w, df[id])
		check(err)
	}
	check(tx.Commit())
	fmt.Printf("indexed %d chunks, %d terms in %v\n", target, len(termID), time.Since(t))

	for _, q := range []string{"multi-head attention", "masked language model pretraining", "positional encoding sine", "BLEU translation English German", "zebra"} {
		t := time.Now()
		res := search(db, q, 10)
		fmt.Printf("query %-40q %v  top: %v\n", q, time.Since(t), res[:min(3, len(res))])
	}
}

type hit struct {
	ID    int64
	Score float64
}

func search(db *sql.DB, q string, k int) []hit {
	var n int
	var avg float64
	check(db.QueryRow(`SELECT count(*), avg(len) FROM chunks`).Scan(&n, &avg))
	const k1, b = 1.2, 0.75
	scores := map[int64]float64{}
	for _, w := range tokenize(q) {
		var id int64
		var dfv int
		if err := db.QueryRow(`SELECT id, df FROM terms WHERE term = ?`, w).Scan(&id, &dfv); err != nil {
			continue
		}
		idf := math.Log(1 + (float64(n)-float64(dfv)+0.5)/(float64(dfv)+0.5))
		rows, err := db.Query(`SELECT p.chunk_id, p.tf, c.len FROM postings p JOIN chunks c ON c.id = p.chunk_id WHERE p.term_id = ?`, id)
		check(err)
		for rows.Next() {
			var cid int64
			var tf, l int
			check(rows.Scan(&cid, &tf, &l))
			scores[cid] += idf * float64(tf) * (k1 + 1) / (float64(tf) + k1*(1-b+b*float64(l)/avg))
		}
		rows.Close()
	}
	out := make([]hit, 0, len(scores))
	for id, s := range scores {
		out = append(out, hit{id, math.Round(s*100) / 100})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

func tokenize(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := f[:0]
	for _, w := range f {
		if len(w) > 1 {
			out = append(out, w)
		}
	}
	return out
}

func chunksFromPDFs(paths []string) []string {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	check(err)
	defer pool.Close()
	inst, err := pool.GetInstance(30 * time.Second)
	check(err)
	var chunks []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		check(err)
		doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
		check(err)
		pc, _ := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		for i := 0; i < pc.PageCount; i++ {
			r, err := inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}})
			check(err)
			words := strings.Fields(r.Text)
			for j := 0; j < len(words); j += 80 {
				chunks = append(chunks, strings.Join(words[j:min(j+80, len(words))], " "))
			}
		}
		inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	}
	return chunks
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
