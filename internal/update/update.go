// Package update checks GitHub for newer ragalay releases and installs
// them (plan1 G19). There is no telemetry: the only request is the public
// "latest release" lookup, at most once a day, and it can be turned off
// with [update] check = false.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "satlavida/ragalay"

// API is the GitHub API base URL (tests replace it).
var API = "https://api.github.com"

// Interval is how often Check asks GitHub.
const Interval = 24 * time.Hour

// Release is the part of a GitHub release ragalay uses.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// Check returns the latest release tag if it is newer than current, or ""
// when current is up to date, a development build, or the last check was
// less than Interval ago and found nothing newer.
func Check(ctx context.Context, current, cacheDir string) (string, error) {
	if current == "" || current == "dev" {
		return "", nil
	}
	path := filepath.Join(cacheDir, "update.json")
	var c cacheFile
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &c) == nil && time.Since(c.CheckedAt) < Interval {
		if Newer(c.Latest, current) {
			return c.Latest, nil
		}
		return "", nil
	}
	rel, err := Latest(ctx)
	if err != nil {
		return "", err
	}
	c = cacheFile{CheckedAt: time.Now(), Latest: rel.Tag}
	if b, err := json.Marshal(c); err == nil {
		os.MkdirAll(cacheDir, 0o755)
		os.WriteFile(path, b, 0o644)
	}
	if Newer(rel.Tag, current) {
		return rel.Tag, nil
	}
	return "", nil
}

// ErrNoRelease means the repository has no published release yet.
var ErrNoRelease = errors.New("no ragalay release has been published yet")

// Latest fetches the latest release.
func Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ragalay")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub said %s", resp.Status)
	}
	var r Release
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Newer reports whether version a is newer than b ("v1.2.3" style; a
// pre-release suffix like "-rc1" sorts before the release).
func Newer(a, b string) bool {
	pa, preA := parse(a)
	pb, preB := parse(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return preA == "" && preB != "" // same numbers: the release beats its pre-release
}

func parse(v string) ([]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil, ""
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, ""
		}
		out[i] = n
	}
	return out, pre
}
