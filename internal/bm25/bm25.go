// Package bm25 is ragalay's keyword search: one tokenizer shared by
// indexing and querying, and the BM25 scoring formula. Postings live in the
// Turso database (store.ReplaceChunks); Turso has no full-text index of its
// own (plan1 §3.1).
package bm25

import (
	"math"
	"strings"
	"unicode"
)

// Standard BM25 parameters.
const (
	K1 = 1.2
	B  = 0.75
)

// stopwords are very common English words that only add noise.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"for": true, "from": true, "has": true, "in": true, "is": true, "it": true, "its": true, "of": true,
	"on": true, "or": true, "that": true, "the": true, "this": true, "to": true, "was": true, "were": true,
	"will": true, "with": true,
}

// Tokenize lowercases text and splits it into words made of letters and
// digits, dropping one-character words and stopwords. Text in scripts
// without spaces (Chinese, Japanese) is split per character so it can
// still be found.
func Tokenize(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 1 {
			w := string(cur)
			if !stopwords[w] {
				out = append(out, w)
			}
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Counts returns term frequencies for text.
func Counts(text string) map[string]int {
	tf := map[string]int{}
	for _, t := range Tokenize(text) {
		tf[t]++
	}
	return tf
}

// IDF is the BM25 inverse document frequency (always positive).
func IDF(n, df int) float64 {
	return math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
}

// Score is one term's BM25 contribution for a chunk of length docLen.
func Score(idf float64, tf, docLen int, avgLen float64) float64 {
	if avgLen <= 0 {
		avgLen = 1
	}
	f := float64(tf)
	return idf * f * (K1 + 1) / (f + K1*(1-B+B*float64(docLen)/avgLen))
}
