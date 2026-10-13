package index

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/satlavida/ragalay/internal/scan"
	"github.com/satlavida/ragalay/internal/store"
)

// Plan 6: progress is counted in work units so a 400-page PDF weighs more
// than a note. Seconds per unit are learned per kind and kept in meta.
const (
	textUnitBytes = 4 << 10   // one unit of text or code
	pdfPageBytes  = 80 << 10  // rough size of a PDF page, until real pages are known
	audioBytesSec = 16 << 10  // rough bytes per second of compressed audio (128 kbit/s)
	videoBytesSec = 256 << 10 // rough bytes per second of video (2 Mbit/s); media units are seconds
	warmDocs      = 3         // documents of a kind before its rate is trusted
	rateMetaKey   = "eta_rate_"
)

// defaultRate is seconds per unit before anything has been measured.
var defaultRate = map[string]float64{
	scan.KindMarkdown: 0.3,
	scan.KindCode:     0.3,
	scan.KindPDF:      1,
	scan.KindImage:    1,
	scan.KindAudio:    0.05, // seconds of work per second of audio
	scan.KindVideo:    0.5,
}

// cost returns the work units of a job (at least 1).
func cost(j store.Job) float64 {
	per := int64(textUnitBytes)
	switch j.Kind {
	case scan.KindImage:
		return 1
	case scan.KindPDF:
		per = pdfPageBytes
	case scan.KindAudio:
		per = audioBytesSec
	case scan.KindVideo:
		per = videoBytesSec
	}
	return max(1, float64(j.Size)/float64(per))
}

// rates learns seconds per unit with an exponentially weighted average, so a
// new device or model adapts within a few documents.
type rates struct {
	rate  map[string]float64
	count map[string]int // documents seen this run, or 1 for a saved rate
	seen  map[string]int // documents timed in this run
}

func newRates(root string) *rates {
	r := &rates{rate: map[string]float64{}, count: map[string]int{}, seen: map[string]int{}}
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		for k := range defaultRate {
			if v, _ := store.Meta(ctx, db, rateMetaKey+k); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
					r.rate[k], r.count[k] = f, warmDocs
				}
			}
		}
		return nil
	})
	return r
}

// observe records that a job of the given cost took d.
func (r *rates) observe(kind string, units float64, d time.Duration) {
	if units <= 0 || d <= 0 {
		return
	}
	s := d.Seconds() / units
	if _, ok := r.rate[kind]; !ok || r.seen[kind] == 0 && r.count[kind] < warmDocs {
		r.rate[kind] = s
	} else {
		const alpha = 0.2
		r.rate[kind] = (1-alpha)*r.rate[kind] + alpha*s
	}
	r.seen[kind]++
	r.count[kind] = max(r.count[kind], r.seen[kind])
}

func (r *rates) perUnit(kind string) float64 {
	if v, ok := r.rate[kind]; ok {
		return v
	}
	if v, ok := defaultRate[kind]; ok {
		return v
	}
	return 1
}

// estimating reports whether any waiting kind has too few timed documents.
func (r *rates) estimating(jobs []store.Job) bool {
	for _, j := range jobs {
		if r.count[j.Kind] < warmDocs {
			return true
		}
	}
	return false
}

// remaining estimates the time for jobs, whose costs are in units; partial
// is the fraction of the first job already done.
func (r *rates) remaining(jobs []store.Job, costs []float64, partial float64) time.Duration {
	var s float64
	for i, j := range jobs {
		c := costs[i]
		if i == 0 {
			c *= 1 - min(max(partial, 0), 1)
		}
		s += c * r.perUnit(j.Kind)
	}
	return time.Duration(s * float64(time.Second))
}

func (r *rates) save(root string) {
	ctx := context.Background()
	store.With(ctx, dbPath(root), func(db *sql.DB) error {
		return store.Tx(ctx, db, func(tx *sql.Tx) error {
			for k, v := range r.rate {
				if err := store.SetMeta(ctx, tx, rateMetaKey+k, strconv.FormatFloat(v, 'f', 4, 64)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
