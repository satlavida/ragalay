package embed

import (
	"math"
	"testing"
)

func TestFitTruncatesAndNormalizes(t *testing.T) {
	v := []float32{3, 4, 100, 100}
	got, err := Fit(v, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || math.Abs(float64(got[0])-0.6) > 1e-6 || math.Abs(float64(got[1])-0.8) > 1e-6 {
		t.Fatalf("Fit = %v, want [0.6 0.8]", got)
	}
	if _, err := Fit([]float32{1}, 2); err == nil {
		t.Fatal("short vector should fail")
	}
	if _, err := Fit([]float32{0, 0}, 2); err == nil {
		t.Fatal("zero vector should fail")
	}
}

func TestBlobRoundTripAndCosine(t *testing.T) {
	v := []float32{0.5, -0.25, 1e-7, 3}
	got, err := FromBlob(Blob(v))
	if err != nil || len(got) != len(v) {
		t.Fatal(err)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("round trip %v != %v", got, v)
		}
	}
	if c := Cosine(v, v); math.Abs(c-1) > 1e-9 {
		t.Fatalf("cosine(v, v) = %v", c)
	}
	if _, err := FromBlob([]byte{1, 2, 3}); err == nil {
		t.Fatal("odd blob should fail")
	}
}

func TestID(t *testing.T) {
	if got := ID("jinaai/m", "e3ae4b6e4af4ec", 512); got != "jinaai/m@e3ae4b6:512" {
		t.Fatalf("ID = %s", got)
	}
}
