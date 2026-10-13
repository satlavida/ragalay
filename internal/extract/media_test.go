package extract

import (
	"context"
	"errors"
	"testing"

	"github.com/satlavida/ragalay/internal/embed"
)

func TestWindows(t *testing.T) {
	cases := []struct {
		dur  int64
		want []Window
	}{
		{0, nil},
		{500, []Window{{0, 500}}},
		{30_000, []Window{{0, 30_000}}},
		{31_000, []Window{{0, 30_000}, {25_000, 31_000}}},
		{70_000, []Window{{0, 30_000}, {25_000, 55_000}, {50_000, 70_000}}},
	}
	for _, c := range cases {
		got := Windows(c.dur)
		if len(got) != len(c.want) {
			t.Errorf("Windows(%d) = %v, want %v", c.dur, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Windows(%d)[%d] = %v, want %v", c.dur, i, got[i], c.want[i])
			}
		}
	}
	// Every instant of a long file is inside some window, and none is longer than 30 s.
	ws := Windows(3_725_000)
	var covered int64
	for _, w := range ws {
		if w.EndMS-w.StartMS > WindowMS || w.StartMS > covered {
			t.Fatalf("bad window %v after covering %d", w, covered)
		}
		covered = max(covered, w.EndMS)
	}
	if covered != 3_725_000 {
		t.Errorf("covered %d", covered)
	}
}

func TestClock(t *testing.T) {
	for ms, want := range map[int64]string{0: "0:00", 59_999: "0:59", 65_000: "1:05", 3_725_000: "1:02:05"} {
		if got := Clock(ms); got != want {
			t.Errorf("Clock(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestMediaUnits(t *testing.T) {
	probe := func(info embed.MediaInfo, err error) Options {
		return Options{Probe: func(context.Context, string) (embed.MediaInfo, error) { return info, err }}
	}
	e := New(t.TempDir(), probe(embed.MediaInfo{DurationMS: 70_000, HasAudio: true, HasVideo: true}, nil))
	res, err := e.Extract(context.Background(), "video", "clips/My_talk.mp4", false)
	if err != nil {
		t.Fatal(err)
	}
	var v, a int
	for _, u := range res.Units {
		switch u.Modality {
		case embed.Video:
			v++
		case embed.Audio:
			a++
		}
		if u.Source != "clips/My_talk.mp4" || u.Text != "My talk" || u.EndMS <= u.StartMS {
			t.Errorf("unit %+v", u)
		}
	}
	if v != 3 || a != 3 || res.Seconds != 70 {
		t.Errorf("video windows %d, audio windows %d, seconds %v", v, a, res.Seconds)
	}
	if res.Units[0].HeadingPath != "0:00-0:30" {
		t.Errorf("label %q", res.Units[0].HeadingPath)
	}

	// A video without sound has frames only; audio without a track fails clearly.
	e = New(t.TempDir(), probe(embed.MediaInfo{DurationMS: 20_000, HasVideo: true}, nil))
	if res, err := e.Extract(context.Background(), "video", "v.mp4", false); err != nil || len(res.Units) != 1 || res.Units[0].Modality != embed.Video {
		t.Errorf("silent video: %+v, %v", res.Units, err)
	}
	e = New(t.TempDir(), probe(embed.MediaInfo{DurationMS: 20_000, HasVideo: true}, nil))
	if _, err := e.Extract(context.Background(), "audio", "a.mp3", false); err == nil {
		t.Error("audio file without an audio track should fail")
	}
	e = New(t.TempDir(), probe(embed.MediaInfo{}, errors.New("boom")))
	if _, err := e.Extract(context.Background(), "audio", "a.mp3", false); err == nil {
		t.Error("probe error should fail")
	}
	if _, err := New(t.TempDir(), Options{}).Extract(context.Background(), "audio", "a.mp3", false); !errors.Is(err, ErrNoProbe) {
		t.Errorf("no probe: %v", err)
	}
}
