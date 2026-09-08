//go:build openbsd

package storage_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/public-shares/cache"
	"filees/public-shares/storage"
)

func TestMaintenanceUnderLockedUnveil(t *testing.T) {
	root := os.Getenv("FILEES_M48_SANDBOX_ROOT")
	if root == "" {
		root = t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaintenanceUnderLockedUnveil$")
		command.Env = append(os.Environ(), "FILEES_M48_SANDBOX_ROOT="+root)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("sandbox: %v\n%s", err, output)
		}
		return
	}
	owner, err := storage.Own(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := obsandbox.Apply(obsandbox.Profile{Name: "public-storage-test", Promises: "stdio rpath wpath cpath fattr flock", Paths: []obsandbox.Path{{Label: "private-root", Name: root, Perms: "rwc"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	store := &cache.Store{Config: cache.Config{Root: root, TTL: time.Second, MaxSize: 1024}}
	body := "sandbox bytes"
	sum := md5.Sum([]byte(body))
	key := strings.Repeat("a", 64)
	issued := time.Now().Add(-time.Hour)
	if err := store.Put(key, strings.NewReader(body), int64(len(body)), hex.EncodeToString(sum[:]), issued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := store.Pin(key, issued)
	if err != nil {
		t.Fatal(err)
	}
	staging := &storage.Staging{Root: root}
	file, release, err := staging.Create()
	if err != nil {
		t.Fatal(err)
	}
	m := &storage.Maintenance{Root: root, Sweep: func(ctx context.Context, now time.Time) (storage.SweepResult, error) {
		r, err := store.Sweep(ctx, now)
		if err != nil {
			return r, err
		}
		s, err := staging.Sweep(ctx, now)
		r.Add(s)
		return r, err
	}}
	if err := m.Pass(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := storage.CheckMaintenance(root, time.Minute, time.Now())
	if err != nil || status.Active != 2 {
		t.Fatalf("pinned status=%+v %v", status, err)
	}
	reader, err := lease.Open()
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	lease.Close()
	file.Close()
	release()
	if err := m.Pass(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err = storage.CheckMaintenance(root, time.Minute, time.Now())
	if err != nil || status.Files != 3 {
		t.Fatalf("cleanup status=%+v %v", status, err)
	}
	after, err := os.Stat(root)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("root changed", err)
	}
}
