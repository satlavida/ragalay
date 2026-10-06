package download

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// rangeServer serves body, honouring Range requests, and counts requests.
func rangeServer(t *testing.T, body []byte, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		start := 0
		if rg := r.Header.Get("Range"); rg != "" {
			start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			if start >= len(body) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(len(body)-1)+"/"+strconv.Itoa(len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.Write(body[start:])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFileDownloadsVerifiesAndSkips(t *testing.T) {
	body := []byte(strings.Repeat("ragalay ", 5000))
	var n atomic.Int32
	srv := rangeServer(t, body, &n)
	dest := filepath.Join(t.TempDir(), "sub", "f.bin")

	var last int64
	err := File(context.Background(), srv.URL, dest, digest(body), func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) || last != int64(len(body)) {
		t.Fatalf("bad download (progress %d)", last)
	}
	// Second call: digest already matches, no request.
	if err := File(context.Background(), srv.URL, dest, digest(body), nil); err != nil || n.Load() != 1 {
		t.Fatalf("expected skip, err=%v requests=%d", err, n.Load())
	}
}

func TestFileResumesPartialDownload(t *testing.T) {
	body := []byte(strings.Repeat("0123456789", 1000))
	var n atomic.Int32
	srv := rangeServer(t, body, &n)
	dest := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(dest+".part", body[:4321], 0o644)

	var first int64 = -1
	err := File(context.Background(), srv.URL, dest, digest(body), func(done, total int64) {
		if first < 0 {
			first = done
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 4321 {
		t.Fatalf("download did not resume from the partial file (first progress %d)", first)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Fatal("resumed file is wrong")
	}
}

func TestFileRejectsBadChecksum(t *testing.T) {
	var n atomic.Int32
	srv := rangeServer(t, []byte("tampered"), &n)
	dest := filepath.Join(t.TempDir(), "f.bin")
	err := File(context.Background(), srv.URL, dest, digest([]byte("expected")), nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a file with a bad checksum must not be installed")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("a bad partial file must be removed")
	}
}

func TestFileRetriesDroppedConnection(t *testing.T) {
	old := RetryDelay
	RetryDelay = 10 * time.Millisecond
	defer func() { RetryDelay = old }()

	body := []byte(strings.Repeat("abcdefghij", 20000))
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			// First attempt: send half the body, then kill the connection.
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body[:len(body)/2])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		start, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-"))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start:])
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := File(context.Background(), srv.URL, dest, digest(body), nil); err != nil {
		t.Fatalf("download should survive a dropped connection: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) || n.Load() != 2 {
		t.Fatalf("bad result after retry (requests %d)", n.Load())
	}
}

func TestFileDoesNotRetryNotFound(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	err := File(context.Background(), srv.URL, filepath.Join(t.TempDir(), "f"), "", nil)
	if err == nil || !strings.Contains(err.Error(), "404") || n.Load() != 1 {
		t.Fatalf("404 must fail once without retries: %v (requests %d)", err, n.Load())
	}
}

func TestExtractZipStripsPrefixAndBlocksTraversal(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.zip")
	writeZip(t, good, map[string]string{"pkg/bin/uv.exe": "uv", "pkg/README": "hi"})
	out := filepath.Join(dir, "out")
	if err := Extract(good, out, "pkg/"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "bin", "uv.exe")); string(b) != "uv" {
		t.Fatal("prefix not stripped")
	}

	evil := filepath.Join(dir, "evil.zip")
	writeZip(t, evil, map[string]string{"../escape.txt": "x"})
	if err := Extract(evil, filepath.Join(dir, "out2"), ""); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
}
