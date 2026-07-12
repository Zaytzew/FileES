package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

// -------- Host-wide Gate (K slotów równoległych commitów) --------

// Gate serializes concurrent commits across the host. Acquire returns a release
// function that must be called (via defer) when the commit completes.
// Each Acquire/release pair is independent — safe for concurrent goroutines.
type Gate interface {
	Acquire(ctx context.Context) (release func(), err error)
}

type hostGate struct {
	baseDir string
	k       int
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

func (g *hostGate) Acquire(ctx context.Context) (func(), error) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()

	for {
		for i := 1; i <= g.k; i++ {
			dir := filepath.Join(g.baseDir, "slot."+itoa(i))
			if err := os.Mkdir(dir, 0o755); err == nil {
				return func() { _ = os.Remove(dir) }, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// -------- RepoMutex: 1 commit naraz per repo (na hoście) --------

// RepoMutex ensures at most one commit per repository at a time. Lock returns
// an unlock function that must be called (via defer) when the commit completes.
type RepoMutex interface {
	Lock(ctx context.Context, repoURL string) (unlock func(), err error)
}

type repoMutex struct {
	baseDir string
}

func NewRepoMutex() RepoMutex {
	base := filepath.Join(userHome(), ".filees", "locks", "repo")
	_ = os.MkdirAll(base, 0o755)
	return &repoMutex{baseDir: base}
}

func (m *repoMutex) Lock(ctx context.Context, repoURL string) (func(), error) {
	dir := filepath.Join(m.baseDir, hash(repoURL))
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()

	for {
		if err := os.Mkdir(dir, 0o755); err == nil {
			return func() { _ = os.Remove(dir) }, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
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
