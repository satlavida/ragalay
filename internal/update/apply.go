package update

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/satlavida/ragalay/internal/download"
)

// AssetName is the release archive for this platform, as .goreleaser.yaml
// names it.
func AssetName(goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("ragalay_%s_%s%s", goos, goarch, ext)
}

// Apply downloads rel's archive for this platform, checks it against the
// release's checksums.txt, and replaces the binary at exe. On Windows the
// running file cannot be overwritten, so it is renamed to exe+".old" first
// (and removed on the next update).
func Apply(ctx context.Context, rel Release, exe, workDir string, progress download.Progress) error {
	want := AssetName(runtime.GOOS, runtime.GOARCH)
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return fmt.Errorf("release %s has no %s", rel.Tag, want)
	}
	if sumsURL == "" {
		return fmt.Errorf("release %s has no checksums.txt; not installing an unverified file", rel.Tag)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	sums := filepath.Join(workDir, "checksums-"+rel.Tag+".txt")
	if err := download.File(ctx, sumsURL, sums, "", nil); err != nil {
		return err
	}
	sha, err := checksumFor(sums, want)
	if err != nil {
		return err
	}
	archive := filepath.Join(workDir, want)
	if err := download.File(ctx, archiveURL, archive, sha, progress); err != nil {
		return err
	}
	unpacked := filepath.Join(workDir, "ragalay-"+rel.Tag)
	os.RemoveAll(unpacked)
	if err := download.Extract(archive, unpacked, ""); err != nil {
		return err
	}
	name := "ragalay"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	newBin := filepath.Join(unpacked, name)
	if _, err := os.Stat(newBin); err != nil {
		return fmt.Errorf("the release archive has no %s", name)
	}
	return replace(exe, newBin)
}

func checksumFor(file, name string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name && len(fields[0]) == 64 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt does not list %s", name)
}

// replace swaps the binary at exe for newBin, keeping exe's permissions.
func replace(exe, newBin string) error {
	fi, err := os.Stat(exe)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(newBin)
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, data, fi.Mode().Perm()|0o100); err != nil {
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	if runtime.GOOS == "windows" {
		// A running .exe can be renamed but not overwritten.
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			os.Rename(old, exe)
			return err
		}
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return errors.Join(err, errors.New("could not replace the binary (is the folder writable?)"))
	}
	return nil
}
