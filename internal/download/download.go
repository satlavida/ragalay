// Package download fetches pinned files over HTTPS with resume, progress
// and SHA-256 verification, and unpacks archives.
package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrChecksum means the downloaded bytes do not match the pinned digest.
var ErrChecksum = errors.New("checksum mismatch")

// Progress is called as bytes arrive. total is -1 when unknown.
type Progress func(done, total int64)

// Client is the HTTP client used for downloads; tests replace it.
var Client = &http.Client{Timeout: 0, Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 60 * time.Second,
}}

// File downloads url to dest. If sha256hex is set, the result must match it.
// An existing dest with the right digest is left alone. Partial downloads are
// kept in dest+".part" and resumed with a Range request.
func File(ctx context.Context, url, dest, sha256hex string, progress Progress) error {
	if sha256hex != "" {
		if ok, _ := matches(dest, sha256hex); ok {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	part := dest + ".part"
	if err := fetchWithRetry(ctx, url, part, progress); err != nil {
		return err
	}
	if sha256hex != "" {
		ok, err := matches(part, sha256hex)
		if err != nil {
			return err
		}
		if !ok {
			os.Remove(part) // corrupt or tampered; start from scratch next time
			return fmt.Errorf("%s: %w", url, ErrChecksum)
		}
	}
	return os.Rename(part, dest)
}

// Retries is how many times a dropped download is resumed before giving up.
var Retries = 5

// RetryDelay is the first wait between attempts; it doubles each time.
var RetryDelay = 2 * time.Second

// fetchWithRetry resumes after network errors (dropped connections, resets,
// timeouts). HTTP errors such as 404 are not retried.
func fetchWithRetry(ctx context.Context, url, part string, progress Progress) error {
	delay := RetryDelay
	var err error
	for attempt := 0; attempt <= Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
		err = fetch(ctx, url, part, progress)
		var he *httpError
		if err == nil || ctx.Err() != nil || errors.As(err, &he) && he.code < 500 {
			return err
		}
	}
	return fmt.Errorf("%w (gave up after %d attempts; run setup again to resume)", err, Retries+1)
}

type httpError struct {
	url    string
	code   int
	status string
}

func (e *httpError) Error() string { return fmt.Sprintf("download %s: HTTP %s", e.url, e.status) }

func fetch(ctx context.Context, url, part string, progress Progress) error {
	var offset int64
	if fi, err := os.Stat(part); err == nil {
		offset = fi.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ragalay")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := Client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return nil // already complete; the checksum decides
	case resp.StatusCode == http.StatusOK:
		offset = 0 // server ignored the range: start over
		flags |= os.O_TRUNC
	default:
		return &httpError{url, resp.StatusCode, resp.Status}
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = offset + resp.ContentLength
	}
	w := io.Writer(f)
	if progress != nil {
		w = &progressWriter{w: f, done: offset, total: total, fn: progress}
		progress(offset, total)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	return f.Close()
}

type progressWriter struct {
	w           io.Writer
	done, total int64
	fn          Progress
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if now := time.Now(); now.Sub(p.last) > 200*time.Millisecond || p.done == p.total {
		p.last = now
		p.fn(p.done, p.total)
	}
	return n, err
}

func matches(path, want string) (bool, error) {
	got, err := SHA256(path)
	if err != nil {
		return false, err
	}
	return got == want, nil
}

// SHA256 returns the hex digest of a file.
func SHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
