//go:build !windows

package cache

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func maintenanceFixture(t *testing.T) (*Store, string, string, time.Time) {
	t.Helper()
	s := &Store{Config: Config{Root: t.TempDir(), TTL: time.Second, MaxSize: 1024}}
	key, body, now := strings.Repeat("a", 64), "held bytes", time.Now()
	putLeaf(t, s, key, body, now)
	return s, key, body, now
}
func putLeaf(t *testing.T, s *Store, key, body string, now time.Time) {
	t.Helper()
	sum := md5.Sum([]byte(body))
	if err := s.Put(key, strings.NewReader(body), int64(len(body)), hex.EncodeToString(sum[:]), now); err != nil {
		t.Fatal(err)
	}
}

func TestSweepPreservesReaderAndZIPLeaseAcrossExpiry(t *testing.T) {
	s, key, body, now := maintenanceFixture(t)
	rootBefore, _ := os.Stat(s.Config.Root)
	reader, _, err := s.Open(key, now)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	lease, _, err := s.Pin(key, now)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	expired := now.Add(2 * time.Second)
	if result, err := s.Sweep(context.Background(), expired); err != nil || result.Files != 0 || result.Active != 1 {
		t.Fatalf("active sweep: %+v %v", result, err)
	}
	if _, _, err := s.Open(key, expired); !errors.Is(err, ErrMiss) {
		t.Fatalf("new reader admitted after TTL: %v", err)
	}
	reader.Close()
	file, err := lease.Open()
	if err != nil {
		t.Fatal(err)
	}
	lease.Close() // the open Reader now independently owns the last pin
	if result, err := s.Sweep(context.Background(), expired); err != nil || result.Files != 0 {
		t.Fatalf("reader lost pin: %+v %v", result, err)
	}
	raw, _ := io.ReadAll(file)
	if string(raw) != body {
		t.Fatal("active bytes changed")
	}
	file.Close()
	result, err := s.Sweep(context.Background(), expired)
	if err != nil || result.Entries != 1 || result.Files != 2 || result.Bytes <= int64(len(body)) {
		t.Fatalf("final sweep: %+v %v", result, err)
	}
	rootAfter, _ := os.Stat(s.Config.Root)
	if !os.SameFile(rootBefore, rootAfter) {
		t.Fatal("root inode changed")
	}
	if _, err := lease.Open(); err == nil {
		t.Fatal("closed lease reopened")
	}
}

func TestFreshFetchRenewsPinnedEntryWithoutReplacingData(t *testing.T) {
	s, key, body, now := maintenanceFixture(t)
	lease, _, err := s.Pin(key, now)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	before, _ := os.Stat(s.dataPath(key))
	putLeaf(t, s, key, body, now.Add(2*time.Second))
	after, _ := os.Stat(s.dataPath(key))
	if !os.SameFile(before, after) {
		t.Fatal("pinned data inode replaced")
	}
	reader, _, err := s.Open(key, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	changed := "different"
	sum := md5.Sum([]byte(changed))
	if err := s.Put(key, strings.NewReader(changed), int64(len(changed)), hex.EncodeToString(sum[:]), now.Add(2*time.Second)); err == nil {
		t.Fatal("replaced pinned immutable content")
	}
}

func TestSweepCollectsOnlyRecognizedOrphans(t *testing.T) {
	s, key, _, now := maintenanceFixture(t)
	shard := filepath.Dir(s.dataPath(key))
	orphan := strings.Repeat("a", 63) + "b"
	known := []string{filepath.Join(shard, orphan+".data"), filepath.Join(shard, ".leaf-123.tmp"), filepath.Join(shard, ".metadata-456.tmp")}
	unknown := []string{filepath.Join(shard, "notes.txt"), filepath.Join(shard, ".leaf-custom.tmp"), filepath.Join(shard, "x.json")}
	for _, name := range append(known, unknown...) {
		if err := os.WriteFile(name, []byte("orphan"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Sweep(context.Background(), now)
	if err != nil || result.Files != 3 {
		t.Fatalf("sweep=%+v %v", result, err)
	}
	for _, name := range known {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan remained: %s", name)
		}
	}
	for _, name := range unknown {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("unknown removed: %s", name)
		}
	}
	putLeaf(t, s, strings.Repeat("b", 64), "next", now)
}

func TestSweepRemovesMetadataWithoutDataBeforeTTL(t *testing.T) {
	s, key, _, now := maintenanceFixture(t)
	if err := os.Remove(s.dataPath(key)); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(context.Background(), now)
	if err != nil || result.Files != 1 || result.Entries != 1 {
		t.Fatalf("orphan metadata=%+v %v", result, err)
	}
}

func TestSweepRefusesSymlinksAndReportsRemovalFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions/symlinks")
	}
	s, key, _, now := maintenanceFixture(t)
	out := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(out, []byte("keep"), 0600)
	link := filepath.Join(filepath.Dir(s.dataPath(key)), ".leaf-123.tmp")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sweep(context.Background(), now); err == nil {
		t.Fatal("symlink not reported")
	}
	if raw, _ := os.ReadFile(out); string(raw) != "keep" {
		t.Fatal("outside changed")
	}
	os.Remove(link)
	shard := filepath.Dir(s.dataPath(key))
	if err := os.Chmod(shard, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(shard, 0700)
	result, err := s.Sweep(context.Background(), now.Add(2*time.Second))
	if err == nil || result.Files != 0 {
		t.Fatalf("failed unlink counted as success: %+v %v (run test as non-root)", result, err)
	}
	if _, err := os.Stat(s.metaPath(key)); err != nil {
		t.Fatal("retry metadata removed")
	}
	os.Chmod(shard, 0700)
	if result, err := s.Sweep(context.Background(), now.Add(2*time.Second)); err != nil || result.Entries != 1 {
		t.Fatalf("retry=%+v %v", result, err)
	}
}

func TestSweepDoesNotQueueBehindWriter(t *testing.T) {
	s, _, _, now := maintenanceFixture(t)
	s.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := s.Sweep(context.Background(), now); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("busy not reported")
		}
	case <-time.After(time.Second):
		t.Error("sweep queued behind writer")
	}
	s.mu.Unlock()
}
