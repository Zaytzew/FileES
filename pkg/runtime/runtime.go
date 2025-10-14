package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
//	"errors"
	"os"
	"path/filepath"
	"time"
)

// -------- Host-wide Gate (K slotów równoległych commitów) --------

type Gate interface {
	Acquire(ctx context.Context) error
	Release()
}

type hostGate struct {
	baseDir string
	k       int
	slot    string // mój slot (po Acquire)
}

// NewHostGate tworzy bramkę na K slotów w ~/.filees/locks/global/slot.N
func NewHostGate(k int) Gate {
	if k <= 0 {
		k = 3
	}
	base := filepath.Join(userHome(), ".filees", "locks", "global")
	_ = os.MkdirAll(base, 0o755)
	return &hostGate{baseDir: base, k: k}
}

func (g *hostGate) Acquire(ctx context.Context) error {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()

	for {
		// spróbuj po kolei slot.1..slot.k
		for i := 1; i <= g.k; i++ {
			dir := filepath.Join(g.baseDir, "slot."+itoa(i))
			if err := os.Mkdir(dir, 0o755); err == nil {
				g.slot = dir
				return nil
			}
		}
		// poczekaj lub przerwij
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (g *hostGate) Release() {
	if g.slot != "" {
		_ = os.Remove(g.slot)
		g.slot = ""
	}
}

// -------- RepoMutex: 1 commit naraz per repo (na hoście) --------

type RepoMutex interface {
	Lock(ctx context.Context, repoURL string) error
	Unlock(repoURL string)
}

type repoMutex struct {
	baseDir string
	held    map[string]string // repoKey -> dir
}

func NewRepoMutex() RepoMutex {
	base := filepath.Join(userHome(), ".filees", "locks", "repo")
	_ = os.MkdirAll(base, 0o755)
	return &repoMutex{baseDir: base, held: map[string]string{}}
}

func (m *repoMutex) Lock(ctx context.Context, repoURL string) error {
	key := hash(repoURL)
	dir := filepath.Join(m.baseDir, key)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()

	for {
		if err := os.Mkdir(dir, 0o755); err == nil {
			m.held[key] = dir
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (m *repoMutex) Unlock(repoURL string) {
	key := hash(repoURL)
	if dir, ok := m.held[key]; ok {
		_ = os.Remove(dir)
		delete(m.held, key)
	}
}

// -------- helpers --------

func userHome() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func hash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// szybkie itoa bez fmt
func itoa(i int) string {
	// max 10 cyfr
	var buf [12]byte
	n := len(buf)
	for i >= 10 {
		n--
		q := i / 10
		buf[n] = byte('0' + i - q*10)
		i = q
	}
	n--
	buf[n] = byte('0' + i)
	return string(buf[n:])
}
