package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const legacyLockGrace = 30 * time.Second

var lockSequence atomic.Uint64

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
			if release, acquired, err := tryLockDir(dir); err != nil {
				return nil, err
			} else if acquired {
				return release, nil
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
		if release, acquired, err := tryLockDir(dir); err != nil {
			return nil, err
		} else if acquired {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

const ownerFile = "owner"

func tryLockDir(dir string) (release func(), acquired bool, err error) {
	if err := os.Mkdir(dir, 0o755); err == nil {
		seq := lockSequence.Add(1)
		owner := fmt.Sprintf("%d %d %d\n", os.Getpid(), time.Now().UnixNano(), seq)
		var ownerLock *os.File
		if ownerFileLocksSupported() {
			owner = fmt.Sprintf("v2 %d %d %d\n", os.Getpid(), time.Now().UnixNano(), seq)
			ownerLock, err = createLockedOwnerFile(filepath.Join(dir, ownerFile), []byte(owner))
		} else {
			err = os.WriteFile(filepath.Join(dir, ownerFile), []byte(owner), 0o600)
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, false, err
		}
		return func() { releaseLockDir(dir, owner, ownerLock) }, true, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, false, err
	}

	stale, err := staleLockDir(dir, time.Now())
	if err != nil || !stale {
		return nil, false, err
	}
	staleDir := fmt.Sprintf("%s.stale.%d.%d", dir, os.Getpid(), lockSequence.Add(1))
	if err := os.Rename(dir, staleDir); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	_ = os.RemoveAll(staleDir)
	return nil, false, nil
}

func staleLockDir(dir string, now time.Time) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err == nil {
		fields := strings.Fields(string(b))
		if len(fields) == 4 && fields[0] == "v2" {
			active, lockErr := ownerFileLockActive(filepath.Join(dir, ownerFile))
			if lockErr != nil {
				return false, lockErr
			}
			return !active, nil
		}
		if len(fields) != 3 {
			return oldLockDir(dir, now)
		}
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil || pid <= 0 {
			return oldLockDir(dir, now)
		}
		return !processAlive(pid), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return oldLockDir(dir, now)
}

func oldLockDir(dir string, now time.Time) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return now.Sub(info.ModTime()) >= legacyLockGrace, nil
}

func releaseLockDir(dir, owner string, ownerLock *os.File) {
	if ownerLock != nil {
		defer ownerLock.Close()
	}
	b, err := os.ReadFile(filepath.Join(dir, ownerFile))
	if err != nil || string(b) != owner {
		return
	}
	_ = os.RemoveAll(dir)
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
