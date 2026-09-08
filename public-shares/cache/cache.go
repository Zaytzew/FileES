// Package cache stores verified Public Shares leaves under an explicitly
// configured persistent root, with temporary content. It never decides authority; callers must re-authorize every
// request before using a hit.
package cache

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/public-shares/storage"
)

const metadataSchema = "filees.public-share-cache/v1"

var ErrMiss = errors.New("public share cache miss")

type Config struct {
	Root    string
	TTL     time.Duration
	MaxSize int64
}

type Store struct {
	Config Config
	mu     sync.Mutex
	pins   map[string]int
}

type metadata struct {
	Schema    string    `json:"schema"`
	Key       string    `json:"key"`
	Size      int64     `json:"size"`
	MD5       string    `json:"md5"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) Put(key string, body io.Reader, size int64, expectedMD5 string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return err
	}
	if err := validKey(key); err != nil {
		return err
	}
	if size < 0 || size > s.Config.MaxSize {
		return errors.New("public share leaf exceeds cache limit")
	}
	if len(expectedMD5) != md5.Size*2 {
		return errors.New("public share cache md5 is invalid")
	}
	if _, err := hex.DecodeString(expectedMD5); err != nil {
		return errors.New("public share cache md5 is invalid")
	}
	if err := s.flushLocked(now.UTC()); err != nil {
		return err
	}
	current, currentErr := s.loadMetadata(key)
	if s.pins[key] != 0 && (currentErr != nil || current.Size != size || current.MD5 != expectedMD5) {
		return errors.New("cannot replace an actively used cache entry with different content")
	}
	used, err := s.usedLocked()
	if err != nil {
		return err
	}
	if currentErr == nil {
		used -= current.Size
	}
	if used > s.Config.MaxSize || size > s.Config.MaxSize-used {
		return errors.New("public share cache capacity exceeded")
	}
	if err := storage.RequireSpace(s.Config.Root, size); err != nil {
		return err
	}
	dir := filepath.Dir(s.dataPath(key))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := s.checkShard(key); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".leaf-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	hash := md5.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(body, size+1))
	if err == nil && written != size {
		err = fmt.Errorf("public share cache size mismatch: got %d want %d", written, size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if err == nil && actual != expectedMD5 {
		err = errors.New("public share cache md5 mismatch")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// A fresh authorized fetch may renew an immutable entry in use, but never
	// replace its data inode while a Reader/ZIP lease still owns it.
	if s.pins[key] == 0 {
		if err := os.Rename(tmpPath, s.dataPath(key)); err != nil {
			return err
		}
	}
	meta := metadata{Schema: metadataSchema, Key: key, Size: size, MD5: expectedMD5, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(s.Config.TTL)}
	if err := atomicMetadata(s.metaPath(key), meta); err != nil {
		if s.pins[key] == 0 {
			_ = os.Remove(s.dataPath(key))
		}
		return err
	}
	return syncDir(dir)
}

func (s *Store) Open(key string, now time.Time) (*Reader, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openLocked(key, now, false)
}

func (s *Store) openLocked(key string, now time.Time, pinned bool) (*Reader, int64, error) {
	if err := s.validate(); err != nil {
		return nil, 0, err
	}
	if err := validKey(key); err != nil {
		return nil, 0, ErrMiss
	}
	meta, err := s.loadMetadata(key)
	if err != nil || (!pinned && !now.UTC().Before(meta.ExpiresAt)) {
		return nil, 0, errors.Join(ErrMiss, s.removeLocked(key))
	}
	if err := s.checkShard(key); err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(s.Config.Root)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	name := filepath.Join("objects", key[:2], key+".data")
	before, err := root.Lstat(name)
	if err != nil {
		return nil, 0, errors.Join(ErrMiss, err)
	}
	if !before.Mode().IsRegular() {
		return nil, 0, errors.New("cache data is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, 0, errors.Join(ErrMiss, s.removeLocked(key))
	}
	info, err := file.Stat()
	if err != nil || info.Size() != meta.Size || !os.SameFile(before, info) {
		file.Close()
		return nil, 0, errors.Join(ErrMiss, s.removeLocked(key))
	}
	s.pinLocked(key)
	return &Reader{File: file, store: s, key: key}, meta.Size, nil
}

func (s *Store) Flush(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return 0, err
	}
	return s.flushCountLocked(now.UTC())
}

func (s *Store) flushLocked(now time.Time) error {
	_, err := s.flushCountLocked(now)
	return err
}

func (s *Store) flushCountLocked(now time.Time) (int, error) {
	result, err := s.sweepLocked(context.Background(), now)
	return int(result.Entries), err
}

func (s *Store) usedLocked() (int64, error) {
	var used int64
	root := filepath.Join(s.Config.Root, "objects")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		key := entry.Name()[:len(entry.Name())-5]
		rel, err := filepath.Rel(root, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if err != nil || len(parts) != 2 || !hexShard(parts[0]) || validKey(key) != nil || key[:2] != parts[0] {
			return nil
		}
		meta, err := s.loadMetadata(key)
		if err != nil {
			return err
		}
		used += meta.Size
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return used, err
}

func (s *Store) loadMetadata(key string) (metadata, error) {
	if validKey(key) != nil {
		return metadata{}, ErrMiss
	}
	if err := s.checkShard(key); err != nil {
		return metadata{}, err
	}
	r, err := os.OpenRoot(s.Config.Root)
	if err != nil {
		return metadata{}, err
	}
	defer r.Close()
	name := filepath.Join("objects", key[:2], key+".json")
	info, err := r.Lstat(name)
	if err != nil {
		return metadata{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return metadata{}, errors.New("unsafe or oversized cache metadata")
	}
	f, err := r.Open(name)
	if err != nil {
		return metadata{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return metadata{}, err
	}
	if len(raw) > 64<<10 {
		return metadata{}, errors.New("oversized cache metadata")
	}
	var meta metadata
	if json.Unmarshal(raw, &meta) != nil || meta.Schema != metadataSchema || meta.Key != key || meta.Size < 0 || meta.ExpiresAt.IsZero() {
		return metadata{}, errors.New("public share cache metadata is invalid")
	}
	return meta, nil
}

func (s *Store) checkShard(key string) error {
	root, err := os.OpenRoot(s.Config.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"objects", filepath.Join("objects", key[:2])} {
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("cache path is not a real directory")
		}
	}
	return nil
}

func (s *Store) removeLocked(key string) error {
	if s.pins[key] != 0 {
		return nil
	}
	r, err := os.OpenRoot(s.Config.Root)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = s.removeEntryLocked(r, key)
	return err
}

func (s *Store) validate() error {
	if !filepath.IsAbs(s.Config.Root) || s.Config.TTL <= 0 || s.Config.MaxSize <= 0 {
		return errors.New("public share cache configuration is incomplete")
	}
	return os.MkdirAll(filepath.Join(s.Config.Root, "objects"), 0700)
}

func validKey(key string) error {
	if len(key) != sha256HexLength {
		return errors.New("public share cache key is invalid")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return errors.New("public share cache key is invalid")
	}
	return nil
}

const sha256HexLength = 64

func (s *Store) dataPath(key string) string {
	return filepath.Join(s.Config.Root, "objects", key[:2], key+".data")
}
func (s *Store) metaPath(key string) string {
	return filepath.Join(s.Config.Root, "objects", key[:2], key+".json")
}

func atomicMetadata(path string, value metadata) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".metadata-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
