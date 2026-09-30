//go:build linux || openbsd

package mobileworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/public-shares/storage"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const readPoolName = "filees-mobile-reads-v1"
const readPoolLock = ".maintenance.lock"

// Lock order: pool admission, then job. Readers retain only the job lock.
// Only the Go worker writes payload via a bounded Writer; SVN gets a pipe,
// never its descriptor/path. Do NOT reuse this GC for child-written spools.
func (s *readSpool) allocate(ctx context.Context, size int64) error {
	base := s.root
	if base == "" {
		base = os.TempDir()
	}
	pool := filepath.Join(base, readPoolName)
	if err := os.MkdirAll(pool, 0700); err != nil {
		return err
	}
	owner, err := ownReadPool(ctx, pool)
	if err != nil {
		return err
	}
	defer owner.Close()
	used, err := sweepReadPool(ctx, pool)
	if err != nil {
		return err
	}
	if used > math.MaxInt64-size {
		return errors.New("read reservations overflow")
	}
	if s.maxBytes > 0 && (used > s.maxBytes || size > s.maxBytes-used) {
		return errors.New("mobile read spool budget exhausted")
	}
	check := s.checkSpace
	if check == nil {
		check = storage.RequireSpace
	}
	// Conservative: already materialized reservations are charged twice in
	// this free-space snapshot. Never undercharge an in-flight writer.
	if err := check(pool, used+size); err != nil {
		return err
	}
	name := "read-" + uuid.NewString()
	dir := filepath.Join(pool, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	lease, err := storage.Own(dir)
	if err != nil {
		return err
	}
	accepted := false
	defer func() {
		if !accepted {
			lease.Close()
		}
	}()
	if err := os.WriteFile(filepath.Join(dir, "reserved"), []byte(strconv.FormatInt(size, 10)), 0600); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "payload"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	s.File = f
	s.release = func() error {
		// Close payload BEFORE releasing the lease (including after failed IO).
		if err := lease.Close(); err != nil {
			return err
		}
		gc, err := ownReadPool(context.Background(), pool)
		if err != nil {
			return err
		} // next admission retries cleanup
		defer gc.Close()
		_, err = sweepReadPool(context.Background(), pool)
		return err
	}
	accepted = true
	return nil
}

func ownReadPool(ctx context.Context, pool string) (*storage.Owner, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		owner, err := storage.Own(pool)
		if err == nil || !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			return owner, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Caller holds pool lock. Unknown entries fail closed and are never removed.
// No age/PID inference: acquired job lock is the proof that its writer ended.
func sweepReadPool(ctx context.Context, pool string) (int64, error) {
	root, err := os.OpenRoot(pool)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return 0, err
	}
	var used int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if entry.Name() == readPoolLock {
			continue
		}
		id := strings.TrimPrefix(entry.Name(), "read-")
		u, e := uuid.Parse(id)
		if !entry.IsDir() || e != nil || u.String() != id || entry.Name() != "read-"+id {
			return 0, errors.New("unknown mobile read pool entry")
		}
		job, err := root.OpenRoot(entry.Name())
		if err != nil {
			return 0, err
		}
		n, err := inspectReadJob(job, filepath.Join(pool, entry.Name()))
		job.Close()
		if err != nil {
			return 0, fmt.Errorf("read spool %s: %w", entry.Name(), err)
		}
		if n < 0 { // acquired lease and removed contents, only empty dir remains
			if err := root.Remove(entry.Name()); err != nil {
				return 0, err
			}
			continue
		}
		if n > math.MaxInt64-used {
			return 0, errors.New("read reservations overflow")
		}
		used += n
	}
	return used, nil
}

func inspectReadJob(job *os.Root, path string) (int64, error) {
	lease, err := storage.Own(path)
	active := errors.Is(err, unix.EWOULDBLOCK)
	if err != nil && !active {
		return 0, err
	}
	if lease != nil {
		defer lease.Close()
	}
	f, err := job.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return 0, err
	}
	var actual int64
	for _, entry := range entries {
		if entry.Name() != readPoolLock && entry.Name() != "reserved" && entry.Name() != "payload" {
			return 0, errors.New("unknown read job contents")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return 0, errors.New("unsafe read job contents")
		}
		if entry.Name() == "payload" {
			actual = info.Size()
		}
	}
	if active {
		info, err := job.Lstat("reserved")
		if err != nil || !info.Mode().IsRegular() || info.Size() > 32 {
			return 0, errors.New("unsafe read reservation")
		}
		f, err := job.Open("reserved")
		if err != nil {
			return 0, err
		}
		raw, err := io.ReadAll(io.LimitReader(f, 33))
		f.Close()
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || n < 0 || n < actual {
			return 0, errors.New("invalid read reservation")
		}
		return n, nil
	}
	// Partial creation after a crash is safe too; no active writer can exist.
	for _, name := range []string{"payload", "reserved", readPoolLock} {
		if _, err := storage.RemoveRegular(job, name); err != nil {
			return 0, err
		}
	}
	return -1, nil
}
