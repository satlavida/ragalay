package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.2.0", "v1.2.0-rc1", true},
		{"v1.2.0-rc1", "v1.2.0", false},
		{"v0.9.0", "v1.0.0", false},
		{"garbage", "v1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestCheckCachesForADay(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"tag_name": "v1.3.0", "assets": []}`))
	}))
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	cache := t.TempDir()

	if v, err := Check(context.Background(), "v1.2.0", cache); err != nil || v != "v1.3.0" {
		t.Fatalf("Check = %q %v", v, err)
	}
	if v, _ := Check(context.Background(), "v1.2.0", cache); v != "v1.3.0" || calls.Load() != 1 {
		t.Fatalf("second check should use the cache: %q, %d calls", v, calls.Load())
	}
	if v, _ := Check(context.Background(), "v1.3.0", cache); v != "" {
		t.Fatalf("up to date should report nothing, got %q", v)
	}
	if v, _ := Check(context.Background(), "dev", cache); v != "" || calls.Load() != 1 {
		t.Fatal("development builds never check")
	}
}

func TestNoReleaseYet(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	old := API
	API = srv.URL
	defer func() { API = old }()
	if _, err := Latest(context.Background()); err != ErrNoRelease {
		t.Fatalf("got %v", err)
	}
}
