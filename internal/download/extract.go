package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extract unpacks a .zip or .tar.gz archive into dir. stripPrefix removes
// a leading directory (e.g. "uv-x86_64-apple-darwin/") from every entry.
// Entries that would land outside dir are rejected.
func Extract(archive, dir, stripPrefix string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dir, stripPrefix)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTarGz(archive, dir, stripPrefix)
	}
	return fmt.Errorf("%s: unsupported archive type", archive)
}

func target(dir, name, strip string) (string, bool, error) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if strip != "" {
		if !strings.HasPrefix(name, strip) {
			return "", false, nil
		}
		name = strings.TrimPrefix(name, strip)
	}
	if name == "" {
		return "", false, nil
	}
	p := filepath.Join(dir, filepath.FromSlash(name))
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("archive entry %q escapes the target folder", name)
	}
	return p, true, nil
}

func extractZip(archive, dir, strip string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		p, ok, err := target(dir, f.Name, strip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, f.Mode())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archive, dir, strip string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p, ok, err := target(dir, h.Name, strip)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(p, tr, h.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Shared libraries are often symlinked (libfoo.dylib -> libfoo.1.dylib).
			// Only allow links that stay inside the folder.
			if filepath.IsAbs(h.Linkname) || strings.Contains(h.Linkname, "..") {
				return fmt.Errorf("archive symlink %q -> %q escapes the target folder", h.Name, h.Linkname)
			}
			os.Remove(p)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	perm := mode.Perm() | 0o600
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
