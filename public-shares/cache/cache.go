// Package cache stores verified Public Shares leaves under an explicitly
// temporary root. It never decides authority; callers must re-authorize every
// request before using a hit.
package cache

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	used, err := s.usedLocked()
	if err != nil {
		return err
	}
	if used+size > s.Config.MaxSize {
		return errors.New("public share cache capacity exceeded")
	}
	dir := filepath.Dir(s.dataPath(key))
	if err := os.MkdirAll(dir, 0700); err != nil {
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
	if err := os.Rename(tmpPath, s.dataPath(key)); err != nil {
		return err
	}
	meta := metadata{Schema: metadataSchema, Key: key, Size: size, MD5: expectedMD5, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(s.Config.TTL)}
	if err := atomicMetadata(s.metaPath(key), meta); err != nil {
		_ = os.Remove(s.dataPath(key))
		return err
	}
	return syncDir(dir)
}

func (s *Store) Open(key string, now time.Time) (*os.File, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return nil, 0, err
	}
	if err := validKey(key); err != nil {
		return nil, 0, ErrMiss
	}
	meta, err := s.loadMetadata(key)
	if err != nil || !now.UTC().Before(meta.ExpiresAt) {
		s.removeLocked(key)
		return nil, 0, ErrMiss
	}
	file, err := os.Open(s.dataPath(key))
	if err != nil {
		s.removeLocked(key)
		return nil, 0, ErrMiss
	}
	info, err := file.Stat()
	if err != nil || info.Size() != meta.Size {
		file.Close()
		s.removeLocked(key)
		return nil, 0, ErrMiss
	}
	return file, meta.Size, nil
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
	root := filepath.Join(s.Config.Root, "objects")
	removed := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil
		}
		key := entry.Name()[:len(entry.Name())-len(".json")]
		meta, err := s.loadMetadata(key)
		if err != nil || !now.Before(meta.ExpiresAt) {
			s.removeLocked(key)
			removed++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return removed, err
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
	raw, err := os.ReadFile(s.metaPath(key))
	if err != nil {
		return metadata{}, err
	}
	var meta metadata
	if json.Unmarshal(raw, &meta) != nil || meta.Schema != metadataSchema || meta.Key != key || meta.Size < 0 || meta.ExpiresAt.IsZero() {
		return metadata{}, errors.New("public share cache metadata is invalid")
	}
	return meta, nil
}

func (s *Store) removeLocked(key string) {
	_ = os.Remove(s.dataPath(key))
	_ = os.Remove(s.metaPath(key))
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
