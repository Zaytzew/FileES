package cache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"filees/public-shares/storage"
)

func (s *Store) Sweep(ctx context.Context, now time.Time) (storage.SweepResult, error) {
	// Put may be receiving a large leaf. Maintenance never queues behind it.
	if !s.mu.TryLock() {
		return storage.SweepResult{}, errors.New("cache maintenance busy: active store operation")
	}
	defer s.mu.Unlock()
	return s.sweepLocked(ctx, now)
}

func (s *Store) sweepLocked(ctx context.Context, now time.Time) (storage.SweepResult, error) {
	var result storage.SweepResult
	r, err := os.OpenRoot(s.Config.Root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	info, err := r.Lstat("objects")
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, errors.New("cache objects is not a real directory")
	}
	var failures error
	err = fs.WalkDir(r.FS(), "objects", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			failures = errors.Join(failures, walkErr)
			return nil
		}
		parts := strings.Split(name, "/")
		if entry.IsDir() {
			if len(parts) > 2 || (len(parts) == 2 && !hexShard(parts[1])) {
				return fs.SkipDir
			}
			return nil
		}
		if len(parts) != 3 || !hexShard(parts[1]) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			failures = errors.Join(failures, errors.New("symlink in cache objects"))
			return nil
		}
		base := entry.Name()
		remove := func() {
			removed, e := storage.RemoveRegular(r, name)
			result.Add(removed)
			failures = errors.Join(failures, e)
		}
		if storage.TempName(base, ".leaf-") || storage.TempName(base, ".metadata-") {
			remove()
			return nil
		}
		ext := path.Ext(base)
		if ext != ".json" && ext != ".data" {
			return nil
		}
		key := strings.TrimSuffix(base, ext)
		if validKey(key) != nil || key[:2] != parts[1] {
			return nil
		}
		if ext == ".data" {
			if _, e := r.Lstat(path.Join("objects", parts[1], key+".json")); errors.Is(e, os.ErrNotExist) {
				if s.pins[key] > 0 {
					result.Active++
					return nil
				}
				remove()
			} else if e != nil {
				failures = errors.Join(failures, e)
			}
			return nil
		}
		meta, e := s.loadMetadata(key)
		if e == nil && now.Before(meta.ExpiresAt) {
			data, statErr := r.Lstat(path.Join("objects", parts[1], key+".data"))
			if statErr == nil && data.Mode().IsRegular() && data.Size() == meta.Size {
				return nil
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				failures = errors.Join(failures, statErr)
				return nil
			}
		}
		if s.pins[key] > 0 {
			result.Active++
			return nil
		}
		removed, e := s.removeEntryLocked(r, key)
		result.Add(removed)
		failures = errors.Join(failures, e)
		return nil
	})
	return result, errors.Join(failures, err)
}

func hexShard(value string) bool {
	return len(value) == 2 && strings.Trim(value, "0123456789abcdef") == ""
}

func (s *Store) removeEntryLocked(root *os.Root, key string) (storage.SweepResult, error) {
	var result storage.SweepResult
	if validKey(key) != nil {
		return result, ErrMiss
	}
	for _, dir := range []string{"objects", path.Join("objects", key[:2])} {
		info, err := root.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if !info.IsDir() {
			return result, errors.New("refuse cache removal through non-directory")
		}
	}
	base := path.Join("objects", key[:2], key)
	data, err := storage.RemoveRegular(root, base+".data")
	result.Add(data)
	// Keep metadata after a failed data removal so the next pass can retry.
	if err != nil {
		return result, err
	}
	meta, err := storage.RemoveRegular(root, base+".json")
	result.Add(meta)
	if err == nil {
		result.Entries = 1
	}
	return result, err
}
