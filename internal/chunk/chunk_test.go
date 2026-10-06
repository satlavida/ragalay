package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func words(n int, prefix string) string {
	w := make([]string, n)
	for i := range w {
		w[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return strings.Join(w, " ")
}

func TestSmallSectionIsOneChunk(t *testing.T) {
	got := Split([]Section{{HeadingPath: "A", Text: words(100, "w")}}, 256, 32, Estimate{})
	if len(got) != 1 || got[0].HeadingPath != "A" || got[0].Tokens > 256 {
		t.Fatalf("got %+v", got)
	}
}

func TestLongSectionSplitsWithOverlap(t *testing.T) {
	text := ""
	for i := 0; i < 12; i++ {
		text += fmt.Sprintf("Sentence %d has some words about attention and encoders here. ", i) + words(40, fmt.Sprintf("p%d_", i)) + ".\n\n"
	}
	got := Split([]Section{{HeadingPath: "Long", Text: text}}, 256, 32, Estimate{})
	if len(got) < 3 {
		t.Fatalf("expected several chunks, got %d", len(got))
	}
	for i, c := range got {
		if c.Tokens > 256 {
			t.Errorf("chunk %d has %d tokens", i, c.Tokens)
		}
		if c.HeadingPath != "Long" {
			t.Errorf("chunk %d lost its heading", i)
		}
	}
	// Overlap: each chunk starts with words from the end of the previous one.
	for i := 1; i < len(got); i++ {
		prev := strings.Fields(got[i-1].Text)
		first := strings.Fields(got[i].Text)[0]
		found := false
		for _, w := range prev[max(0, len(prev)-40):] {
			if w == first {
				found = true
			}
		}
		if !found {
			t.Errorf("chunk %d does not overlap chunk %d (starts with %q)", i, i-1, first)
		}
	}
}

func TestHugeSentenceFallsBackToWords(t *testing.T) {
	got := Split([]Section{{Text: words(2000, "x")}}, 128, 16, Estimate{})
	for i, c := range got {
		if c.Tokens > 128 {
			t.Fatalf("chunk %d has %d tokens", i, c.Tokens)
		}
	}
	all := strings.Join(func() []string {
		var s []string
		for _, c := range got {
			s = append(s, c.Text)
		}
		return s
	}(), " ")
	for _, w := range []string{"x0", "x999", "x1999"} {
		if !strings.Contains(all, w+" ") && !strings.HasSuffix(all, w) {
			t.Errorf("word %s lost", w)
		}
	}
}

func TestTinySectionsMergeButNotAcrossPages(t *testing.T) {
	got := Split([]Section{
		{HeadingPath: "Doc > Intro", Text: "Short intro."},
		{HeadingPath: "Doc > Setup", Text: "Install it."},
		{HeadingPath: "Doc > Usage", Text: words(120, "u")},
		{HeadingPath: "", Text: "Page two text.", Page: 2},
	}, 256, 32, Estimate{})
	if len(got) != 2 {
		t.Fatalf("want 2 chunks, got %d: %+v", len(got), got)
	}
	if got[0].HeadingPath != "Doc > Intro" || !strings.Contains(got[0].Text, "## Setup\nInstall it.") || !strings.Contains(got[0].Text, "u119") {
		t.Errorf("merge lost structure: %+v", got[0])
	}
	if got[1].Page != 2 {
		t.Errorf("page boundary crossed: %+v", got[1])
	}
}

func TestEmptySectionsSkipped(t *testing.T) {
	if got := Split([]Section{{Text: "  \n "}, {HeadingPath: "x"}}, 256, 32, Estimate{}); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
