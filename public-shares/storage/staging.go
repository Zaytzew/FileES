package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Staging tracks creation through final Close, including the writer/reopen gap.
// All production authority fetches share this instance.
type Staging struct {
	Root   string
	mu     sync.Mutex
	active map[string]bool
}

func (s *Staging) Create() (*os.File, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.CreateTemp(s.Root, ".public-share-leaf-*.tmp")
	if err != nil {
		return nil, nil, err
	}
	if s.active == nil {
		s.active = make(map[string]bool)
	}
	name := filepath.Base(f.Name())
	s.active[name] = true
	var once sync.Once
	release := func() { once.Do(func() { s.mu.Lock(); delete(s.active, name); s.mu.Unlock() }) }
	return f, release, nil
}

func (s *Staging) Sweep(ctx context.Context, _ time.Time) (SweepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result SweepResult
	r, err := os.OpenRoot(s.Root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	dir, err := r.Open(".")
	if err != nil {
		return result, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return result, err
	}
	var failures error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(failures, err)
		}
		if !TempName(entry.Name(), ".public-share-leaf-") {
			continue
		}
		if s.active[entry.Name()] {
			result.Active++
			continue
		}
		removed, err := RemoveRegular(r, entry.Name())
		result.Add(removed)
		failures = errors.Join(failures, err)
	}
	return result, failures
}
