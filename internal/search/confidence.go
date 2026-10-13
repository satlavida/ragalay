package search

import "github.com/satlavida/ragalay/internal/embed"

// Confidence levels of a hit.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// gap is how far below the best hit's similarity another hit may fall before
// it is dropped in the default mode.
const gap = 0.10

// closestFew is how many weak hits are still shown when nothing matches well.
const closestFew = 3

func level(c string) int {
	switch c {
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	}
	return 0
}

// rate says how sure a match is. Ranking scores (RRF) only order hits and say
// nothing about quality, so the label comes from the cosine similarity,
// judged against the profile's measured thresholds. A hit that also contains
// the query's words moves up one level. "" means the profile has no
// calibration or the search had no vector part.
func rate(p embed.Profile, r Result) string {
	if p.StrongSim == 0 || p.FairSim == 0 || r.Similarity == 0 && !r.kw {
		return ""
	}
	c := Low
	switch {
	case r.Similarity >= p.StrongSim:
		c = High
	case r.Similarity >= p.FairSim:
		c = Medium
	}
	if r.kw {
		switch c {
		case Low:
			c = Medium
		case Medium:
			c = High
		}
	}
	return c
}

// rateAndFilter labels the hits and drops the weak ones as o.Confidence asks.
// By default weak hits only stay when nothing better exists, so a search for
// something that is not in the folder says so instead of listing noise.
func (s *Searcher) rateAndFilter(rs []Result, o Options, notice string) ([]Result, string) {
	p, ok := embed.Lookup(s.Cfg.Embed.Profile)
	if !ok || o.Mode == Keyword {
		return rs, notice
	}
	best := 0
	for i := range rs {
		rs[i].Confidence = rate(p, rs[i])
		best = max(best, level(rs[i].Confidence))
	}
	if best == 0 || len(rs) == 0 {
		return rs, notice
	}
	floor := 0
	switch o.Confidence {
	case "all":
	case "high":
		floor = 3
	case "medium":
		floor = 2
	case "", "auto":
		if best >= 2 {
			floor = 2
		}
	}
	var top float64
	for _, r := range rs {
		top = max(top, r.Similarity)
	}
	auto := o.Confidence == "" || o.Confidence == "auto"
	kept := rs[:0:0]
	for _, r := range rs {
		if level(r.Confidence) < floor {
			continue
		}
		// Next to a clear winner, a hit far below it is noise even when it
		// clears the floor (audio and video score high on unrelated queries).
		if auto && best >= 2 && !r.kw && top >= p.StrongSim && top-r.Similarity > gap {
			continue
		}
		kept = append(kept, r)
	}
	switch {
	case len(kept) == 0:
		notice = joinNotice(notice, "no match at that confidence level; try --confidence all")
	case best == 1 && floor <= 1:
		kept = kept[:min(len(kept), closestFew)]
		notice = joinNotice(notice, "no confident match: these are the closest passages, but they are probably not what you want")
	}
	return kept, notice
}
