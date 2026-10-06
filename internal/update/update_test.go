package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestApplyVerifiesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	// A release archive containing the new binary.
	name := "ragalay"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var zbuf bytes.Buffer
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	if strings.HasSuffix(asset, ".zip") {
		zw := zip.NewWriter(&zbuf)
		w, _ := zw.Create(name)
		w.Write([]byte("new binary"))
		zw.Close()
	} else {
		gz := gzip.NewWriter(&zbuf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 10, Typeflag: tar.TypeReg})
		tw.Write([]byte("new binary"))
		tw.Close()
		gz.Close()
	}
	sum := sha256.Sum256(zbuf.Bytes())
	good := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	sums := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			w.Write([]byte(sums))
		default:
			w.Write(zbuf.Bytes())
		}
	}))
	defer srv.Close()
	rel := Release{Tag: "v9.9.9", Assets: []Asset{{Name: asset, URL: srv.URL + "/" + asset}, {Name: "checksums.txt", URL: srv.URL + "/checksums.txt"}}}

	exe := filepath.Join(dir, name)
	os.WriteFile(exe, []byte("old binary"), 0o755)
	if err := Apply(context.Background(), rel, exe, filepath.Join(dir, "work"), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("binary not replaced: %q", b)
	}

	// A tampered archive is refused.
	sums = strings.Repeat("0", 64) + "  " + asset + "\n"
	os.RemoveAll(filepath.Join(dir, "work2"))
	if err := Apply(context.Background(), rel, exe, filepath.Join(dir, "work2"), nil); err == nil {
		t.Fatal("checksum mismatch must refuse the update")
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatal("a refused update must not touch the binary")
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
