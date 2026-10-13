package search

import (
	"testing"

	"github.com/satlavida/ragalay/internal/config"
	"github.com/satlavida/ragalay/internal/embed"
)

func sim(s float64, kw bool) Result { return Result{Similarity: s, kw: kw} }

func TestRateAndFilter(t *testing.T) {
	g := &Searcher{Cfg: config.Config{Embed: config.Default().Embed}}
	g.Cfg.Embed.Profile = embed.Gemma2
	cases := []struct {
		name   string
		in     []Result
		conf   string
		want   []string // confidence of the kept hits
		notice bool
	}{
		{"clear winner drops the weak and far-below", []Result{sim(.83, false), sim(.68, false), sim(.57, false)}, "", []string{"high"}, false},
		{"close runners-up stay", []Result{sim(.80, false), sim(.72, false), sim(.50, false)}, "", []string{"high", "medium"}, false},
		{"nothing matches well", []Result{sim(.57, false), sim(.56, false)}, "", []string{"low", "low"}, true},
		{"only the closest few", []Result{sim(.57, false), sim(.56, false), sim(.55, false), sim(.54, false), sim(.53, false)}, "", []string{"low", "low", "low"}, true},
		{"words in the passage lift a weak match", []Result{sim(.57, true), sim(.56, false)}, "", []string{"medium"}, false},
		{"all keeps everything", []Result{sim(.83, false), sim(.5, false)}, "all", []string{"high", "low"}, false},
		{"high floor", []Result{sim(.7, false), sim(.8, false)}, "high", []string{"high"}, false},
		{"floor with no match", []Result{sim(.7, false)}, "high", nil, true},
	}
	for _, c := range cases {
		got, notice := g.rateAndFilter(c.in, Options{Confidence: c.conf}, "")
		var labels []string
		for _, r := range got {
			labels = append(labels, r.Confidence)
		}
		if len(labels) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, labels, c.want)
			continue
		}
		for i := range labels {
			if labels[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, labels, c.want)
			}
		}
		if (notice != "") != c.notice {
			t.Errorf("%s: notice %q", c.name, notice)
		}
	}
}

func TestUncalibratedProfileIsUnrated(t *testing.T) {
	s := &Searcher{Cfg: config.Config{Embed: config.Default().Embed}}
	s.Cfg.Embed.Profile = embed.JinaV5
	got, notice := s.rateAndFilter([]Result{sim(.2, false), sim(.1, false)}, Options{}, "")
	if len(got) != 2 || got[0].Confidence != "" || notice != "" {
		t.Fatalf("got %+v %q", got, notice)
	}
}
