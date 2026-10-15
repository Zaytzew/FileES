// Package localallocation measures presentation-only local disk allocation.
// It never reads file contents or interprets SVN/FileES state.
package localallocation

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"
)

const auditAge = 30 * time.Minute
const maxDirectories = 100000

type metadata struct {
	bytes     int64
	directory bool
	stamp     int64
}
type directory struct {
	Bytes    int64     `json:"bytes"`
	Stamp    int64     `json:"stamp"`
	At       time.Time `json:"at"`
	Children []string  `json:"children,omitempty"`
}
type record struct {
	Directories map[string]directory `json:"directories"`
	Value       Value                `json:"value"`
	Hints       map[string]string    `json:"hints,omitempty"`
}

// wait is invoked before each metadata request, directory open and bounded
// enumeration. Production inserts at least 10ms: <=100 work units/s globally,
// not per WC. Reads are serial and yield entirely while the GUI reports work.
type scanner struct {
	probe func(string) (metadata, error)
	wait  func(context.Context) error
}

func safeRel(rel string) bool {
	return rel == "." || (filepath.IsLocal(rel) && filepath.Clean(rel) == rel)
}

func (s scanner) scan(ctx context.Context, root Root, old record, now time.Time) (record, error) {
	next := record{Directories: map[string]directory{}, Hints: root.Hints}
	queue := []string{"."}
	partial := false
	var total int64
	oldest := now
	for len(queue) > 0 {
		if len(next.Directories) >= maxDirectories {
			partial = true
			break
		}
		rel := queue[0]
		queue = queue[1:]
		if !safeRel(rel) {
			partial = true
			continue
		}
		if _, seen := next.Directories[rel]; seen {
			partial = true
			continue
		}
		if err := s.wait(ctx); err != nil {
			return old, err
		}
		path := filepath.Join(root.Path, rel)
		info, err := s.probe(path)
		if err != nil || !info.directory {
			partial = true
			continue
		}
		prior, known := old.Directories[rel]
		dirty := false
		for hint, version := range root.Hints {
			if old.Hints[hint] == version {
				continue
			}
			if hint == rel {
				dirty = true
				break
			}
		}
		current := prior
		if !known || dirty || prior.Stamp != info.stamp || now.Sub(prior.At) >= auditAge || now.Before(prior.At) {
			current = directory{Bytes: info.bytes, Stamp: info.stamp, At: now}
			if err := s.wait(ctx); err != nil {
				return old, err
			}
			f, err := openDirectory(path) // no-follow, share-delete; never document contents
			if err != nil {
				partial = true
				continue
			}
			complete := true
			for {
				if err := s.wait(ctx); err != nil {
					f.Close()
					return old, err
				}
				names, readErr := f.Readdirnames(32)
				for _, name := range names {
					if filepath.Base(name) != name || name == "." || name == ".." {
						complete = false
						continue
					}
					if err := s.wait(ctx); err != nil {
						f.Close()
						return old, err
					}
					childRel := filepath.Join(rel, name)
					child, err := s.probe(filepath.Join(root.Path, childRel))
					if err != nil {
						complete = false
						continue
					}
					if child.directory {
						if len(queue)+len(next.Directories)+len(current.Children) >= maxDirectories {
							complete = false
						} else {
							current.Children = append(current.Children, childRel)
						}
					} else {
						current.Bytes += child.bytes
					}
				}
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) {
						complete = false
					}
					break
				}
			}
			f.Close()
			if !complete {
				partial = true
				current.At = time.Time{}
			} // retry instead of blessing a partial directory
		}
		next.Directories[rel] = current
		if !current.At.IsZero() && current.At.Before(oldest) {
			oldest = current.At
		}
		total += current.Bytes
		queue = append(queue, current.Children...)
	}
	next.Value = Value{Known: len(next.Directories) > 0, Bytes: total, State: "ready", MeasuredAt: oldest.UTC().Format(time.RFC3339)}
	if now.Sub(oldest) >= 5*time.Minute {
		next.Value.State = "stale"
	}
	if partial {
		// Preserve the last total on failure, not a misleading drop in usage.
		if old.Value.Known {
			next.Value = old.Value
		}
		next.Value.State = "partial"
	}
	return next, nil
}
