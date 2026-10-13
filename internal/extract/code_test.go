package extract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/satlavida/ragalay/internal/chunk"
)

func TestCodeSplitsOnBlankLinesWithLineRanges(t *testing.T) {
	var b strings.Builder
	for f := 0; f < 6; f++ {
		fmt.Fprintf(&b, "func f%d() {\n", f)
		for i := 0; i < 8; i++ {
			fmt.Fprintf(&b, "\tdoSomethingUseful(%d, %d)\n", f, i)
		}
		b.WriteString("}\n\n")
	}
	units, err := Code("pkg/a.go", []byte(b.String()), 80, 10, chunk.Estimate{})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) < 3 {
		t.Fatalf("want several passages, got %d", len(units))
	}
	total := strings.Count(b.String(), "\n")
	if !strings.HasPrefix(units[0].HeadingPath, "a.go:1-") {
		t.Errorf("first label %q", units[0].HeadingPath)
	}
	var last int
	for _, u := range units {
		var from, to int
		if _, err := fmt.Sscanf(u.HeadingPath, "a.go:%d-%d", &from, &to); err != nil || from > to {
			t.Fatalf("bad label %q", u.HeadingPath)
		}
		if from > last+1 {
			t.Errorf("gap before %q (previous ended at %d)", u.HeadingPath, last)
		}
		last = max(last, to)
	}
	if last != total {
		t.Errorf("covered up to line %d of %d", last, total)
	}
}

func TestCodeRejectsBinaryAndMinified(t *testing.T) {
	if _, err := Code("x.go", []byte("a\x00b"), 100, 10, chunk.Estimate{}); err == nil {
		t.Error("binary accepted")
	}
	if _, err := Code("x.js", []byte(strings.Repeat("a=1;", 500)), 100, 10, chunk.Estimate{}); err == nil {
		t.Error("minified accepted")
	}
	if _, err := Code("x.go", []byte("\n\n"), 100, 10, chunk.Estimate{}); err == nil {
		t.Error("empty accepted")
	}
}
