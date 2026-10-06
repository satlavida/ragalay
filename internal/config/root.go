package config

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNotInitialized means no .ragalay directory was found.
var ErrNotInitialized = errors.New("not a ragalay directory (run \"ragalay init\" first)")

// FindRoot walks up from start until it finds a directory containing
// .ragalay/, like git does with .git/.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, DirName)); err == nil && fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotInitialized
		}
		dir = parent
	}
}

// ResolveRoot picks the root in priority order: explicit (flag or
// RAGALAY_ROOT), then walking up from the working directory, then walking up
// from the executable's directory (double-clicked binaries on macOS start in
// the home directory, not next to the binary).
func ResolveRoot(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("RAGALAY_ROOT")
	}
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(filepath.Join(abs, DirName)); err != nil || !fi.IsDir() {
			return "", ErrNotInitialized
		}
		return abs, nil
	}
	if wd, err := os.Getwd(); err == nil {
		if root, err := FindRoot(wd); err == nil {
			return root, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if root, err := FindRoot(filepath.Dir(exe)); err == nil {
			return root, nil
		}
	}
	return "", ErrNotInitialized
}
