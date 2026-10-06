package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/satlavida/ragalay/internal/config"
)

// SafetyRescan is how often Watch rescans even without file events, in case
// the OS dropped some (network drives, overflowed event queues).
var SafetyRescan = 10 * time.Minute

// Watch scans once, then again whenever something changes under the
// scanned folders, until ctx is cancelled. Bursts of events (a copy of many
// files, an editor's save dance) are collapsed with debounce. Changes to
// .ragalay/config.toml reload the config and the set of watched folders.
// The caller must hold the index lock for the whole time.
func Watch(ctx context.Context, root string, debounce time.Duration, onScan func(Report, error)) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	for {
		reload, err := watchOnce(ctx, root, cfg, debounce, onScan)
		if err != nil || !reload {
			return err
		}
		next, err := config.Load(root)
		if err == nil {
			err = next.Validate()
		}
		if err != nil {
			// Keep watching with the previous config until the file is fixed.
			onScan(Report{}, fmt.Errorf("config.toml: %w (still using the previous settings)", err))
			continue
		}
		cfg = next
	}
}

// watchOnce watches with one config. It returns reload=true when the config
// file changed.
func watchOnce(ctx context.Context, root string, cfg config.Config, debounce time.Duration,
	onScan func(Report, error)) (reload bool, err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return false, err
	}
	defer w.Close()
	m, err := NewMatcher(cfg.Scan.Ignore)
	if err != nil {
		return false, err
	}
	stateDir := filepath.Join(root, config.DirName)
	if err := w.Add(stateDir); err != nil {
		return false, err
	}
	for _, folder := range config.EffectiveFolders(cfg.Scan.Folders) {
		addTree(w, root, filepath.Join(root, filepath.FromSlash(folder)), m)
	}

	scan := func() { onScan(Run(ctx, root, cfg)) }
	scan()

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	safety := time.NewTicker(SafetyRescan)
	defer safety.Stop()
	configChanged := false
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case ev, ok := <-w.Events:
			if !ok {
				return false, errors.New("file watcher stopped")
			}
			if filepath.Dir(ev.Name) == stateDir {
				// Ignore the database and lock file; only the config matters.
				if filepath.Base(ev.Name) == config.ConfigFile {
					configChanged = true
					timer.Reset(debounce)
				}
				continue
			}
			rel, err := filepath.Rel(root, ev.Name)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			rel = filepath.ToSlash(rel)
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if !m.Skip(rel, true) {
						addTree(w, root, ev.Name, m) // fsnotify is not recursive
					}
				}
			}
			if m.Skip(rel, false) {
				continue
			}
			timer.Reset(debounce)
		case err, ok := <-w.Errors:
			if !ok {
				return false, errors.New("file watcher stopped")
			}
			onScan(Report{}, err)
		case <-timer.C:
			if configChanged {
				return true, nil
			}
			scan()
		case <-safety.C:
			scan()
		}
	}
}

// addTree watches dir and every folder below it that is not ignored.
func addTree(w *fsnotify.Watcher, root, dir string, m *Matcher) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != dir {
			if rel, err := filepath.Rel(root, p); err == nil && m.Skip(filepath.ToSlash(rel), true) {
				return filepath.SkipDir
			}
		}
		w.Add(p)
		return nil
	})
}
