package bm25

import (
	"slices"
	"testing"
)

func TestTokenize(t *testing.T) {
	got := Tokenize("The Transformer's multi-head attention (BLEU 28.4) is a model — 注意力")
	want := []string{"transformer", "multi", "head", "attention", "bleu", "28", "model", "注", "意", "力"}
	if !slices.Equal(got, want) {
		t.Fatalf("Tokenize = %v\nwant       %v", got, want)
	}
}

func TestScoreOrdering(t *testing.T) {
	rare, common := IDF(1000, 3), IDF(1000, 600)
	if rare <= common || common <= 0 {
		t.Fatalf("idf rare=%v common=%v", rare, common)
	}
	if Score(rare, 3, 100, 100) <= Score(rare, 1, 100, 100) {
		t.Fatal("more occurrences should score higher")
	}
	if Score(rare, 1, 50, 100) <= Score(rare, 1, 400, 100) {
		t.Fatal("shorter chunks should score higher for the same tf")
	}
}
