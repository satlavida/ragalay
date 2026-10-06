// Package lock is the single-indexer lock (plan1 G12): only one ragalay
// process indexes a folder at a time. Readers never take it.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileName is the lock file inside .ragalay/.
const FileName = "index.lock"

// Info is the lock file's content.
type Info struct {
	PID     int       `json:"pid"`
	Command string    `json:"command"`
	Started time.Time `json:"started"`
}

// HeldError means another live process holds the lock.
type HeldError struct{ Info Info }

func (e *HeldError) Error() string {
	return fmt.Sprintf("another ragalay process (pid %d, %q) has been indexing since %s",
		e.Info.PID, e.Info.Command, e.Info.Started.Local().Format("15:04:05"))
}

// Lock is a held index lock.
type Lock struct{ path string }

// Acquire takes the lock in dir (the .ragalay folder). A lock left behind
// by a process that no longer runs is taken over.
func Acquire(dir, command string) (*Lock, error) {
	path := filepath.Join(dir, FileName)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			info := Info{PID: os.Getpid(), Command: command, Started: time.Now()}
			werr := json.NewEncoder(f).Encode(info)
			cerr := f.Close()
			if err := errors.Join(werr, cerr); err != nil {
				os.Remove(path)
				return nil, err
			}
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		holder, alive := Read(dir)
		if alive {
			return nil, &HeldError{Info: holder}
		}
		// Stale: the holder crashed or was killed.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, errors.New("could not take the index lock")
}

// Release removes the lock file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	err := os.Remove(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Read returns the lock holder and whether that process is still running.
func Read(dir string) (Info, bool) {
	var info Info
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return info, false
	}
	if json.Unmarshal(b, &info) != nil || info.PID <= 0 {
		// Unreadable lock: treat as held for a short grace period in case the
		// writer is mid-write, then as stale.
		fi, err := os.Stat(filepath.Join(dir, FileName))
		return info, err == nil && time.Since(fi.ModTime()) < 5*time.Second
	}
	return info, processAlive(info.PID)
}
