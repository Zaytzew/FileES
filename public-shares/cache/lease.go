package cache

import (
	"errors"
	"os"
	"sync"
	"time"
)

// Reader pins the entry until Close, including slow HTTP/Range transfers.
type Reader struct {
	*os.File
	store *Store
	key   string
	once  sync.Once
	err   error
}

func (r *Reader) Close() error {
	r.once.Do(func() { r.err = r.File.Close(); r.store.unpin(r.key) })
	return r.err
}

// Lease pins a prepared ZIP leaf without keeping a descriptor per leaf.
// Only this admitted use can open it after TTL; new callers still miss.
type Lease struct {
	store  *Store
	key    string
	mu     sync.Mutex
	closed bool
}

func (s *Store) Pin(key string, now time.Time) (*Lease, int64, error) {
	s.mu.Lock()
	reader, size, err := s.openLocked(key, now, false)
	if err != nil {
		s.mu.Unlock()
		return nil, 0, err
	}
	s.pinLocked(key)
	s.mu.Unlock()
	if err := reader.Close(); err != nil {
		s.unpin(key)
		return nil, 0, err
	}
	return &Lease{store: s, key: key}, size, nil
}

func (l *Lease) Open() (*Reader, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("cache lease is closed")
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	reader, _, err := l.store.openLocked(l.key, time.Time{}, true)
	return reader, err
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.store.unpin(l.key)
	}
	return nil
}

func (s *Store) pinLocked(key string) {
	if s.pins == nil {
		s.pins = make(map[string]int)
	}
	s.pins[key]++
}
func (s *Store) unpin(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins[key]--
	if s.pins[key] == 0 {
		delete(s.pins, key)
	}
}
