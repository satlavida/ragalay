package extract

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/satlavida/ragalay/internal/embed"
	"github.com/satlavida/ragalay/internal/scan"
)

// Media windows (plan3 D2). The model's processor cuts audio at 30 s, and a
// video window is 30 frames at one frame per second, so both use 30 s windows
// that overlap by 5 s: a sound or scene at a boundary lands whole in one.
const (
	WindowMS  = 30_000
	OverlapMS = 5_000
)

// Window is a time range of a media file, in milliseconds.
type Window struct{ StartMS, EndMS int64 }

// Windows splits a duration into overlapping windows. A duration of zero or
// less has none.
func Windows(durationMS int64) []Window {
	var out []Window
	for start := int64(0); durationMS > 0; start += WindowMS - OverlapMS {
		end := min(start+WindowMS, durationMS)
		out = append(out, Window{start, end})
		if end >= durationMS {
			break
		}
	}
	return out
}

// Clock formats milliseconds as m:ss, or h:mm:ss from one hour.
func Clock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Range formats a window as "0:25-0:55".
func Range(w Window) string { return Clock(w.StartMS) + "-" + Clock(w.EndMS) }

// ErrNoProbe means the extractor has no way to read media files.
var ErrNoProbe = errors.New("media files need the indexing model to read them")

// mediaUnits plans the chunks of an audio or video file. A video gets
// frame windows and, when it has sound, audio windows from its audio track.
func (e *Extractor) mediaUnits(ctx context.Context, kind, rel, file string) (Result, error) {
	if e.Opts.Probe == nil {
		return Result{}, ErrNoProbe
	}
	info, err := e.Opts.Probe(ctx, file)
	if err != nil {
		return Result{}, fmt.Errorf("cannot read this file: %w", err)
	}
	if info.DurationMS <= 0 {
		return Result{}, errors.New("cannot tell how long this file is")
	}
	res := Result{Seconds: float64(info.DurationMS) / 1000}
	// The file name is searchable by keyword, like a picture's.
	name := strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSuffix(path.Base(rel), path.Ext(rel)))
	add := func(modality string) {
		for _, w := range Windows(info.DurationMS) {
			res.Units = append(res.Units, Unit{Modality: modality, Text: name, HeadingPath: Range(w),
				Source: rel, StartMS: w.StartMS, EndMS: w.EndMS})
		}
	}
	switch kind {
	case scan.KindAudio:
		if !info.HasAudio {
			return res, errors.New("the file has no audio track")
		}
		add(embed.Audio)
	default:
		if !info.HasVideo {
			return res, errors.New("the file has no video track")
		}
		add(embed.Video)
		if info.HasAudio {
			add(embed.Audio)
		}
	}
	return res, nil
}
