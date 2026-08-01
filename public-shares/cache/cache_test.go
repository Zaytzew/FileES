package cache

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPutOpenAndFlush(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	store := &Store{Config: Config{Root: t.TempDir(), TTL: 12 * time.Hour, MaxSize: 1024}}
	key := strings.Repeat("a", 64)
	body := "verified leaf"
	digest := md5.Sum([]byte(body))
	if err := store.Put(key, strings.NewReader(body), int64(len(body)), hex.EncodeToString(digest[:]), now); err != nil {
		t.Fatal(err)
	}
	file, size, err := store.Open(key, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(file)
	file.Close()
	if string(raw) != body || size != int64(len(body)) {
		t.Fatalf("hit = %q size=%d", raw, size)
	}
	if removed, err := store.Flush(now.Add(12 * time.Hour)); err != nil || removed != 1 {
		t.Fatalf("flush = %d, %v", removed, err)
	}
	if _, _, err := store.Open(key, now.Add(13*time.Hour)); !errors.Is(err, ErrMiss) {
		t.Fatalf("expired entry = %v", err)
	}
}

func TestMismatchNeverBecomesHit(t *testing.T) {
	store := &Store{Config: Config{Root: t.TempDir(), TTL: time.Hour, MaxSize: 1024}}
	key := strings.Repeat("b", 64)
	if err := store.Put(key, strings.NewReader("corrupt"), 7, strings.Repeat("0", 32), time.Now()); err == nil {
		t.Fatal("md5 mismatch accepted")
	}
	if _, _, err := store.Open(key, time.Now()); !errors.Is(err, ErrMiss) {
		t.Fatalf("failed staging became a cache hit: %v", err)
	}
}

func TestCapacityIsHardBound(t *testing.T) {
	store := &Store{Config: Config{Root: t.TempDir(), TTL: time.Hour, MaxSize: 4}}
	body := "12345"
	digest := md5.Sum([]byte(body))
	if err := store.Put(strings.Repeat("c", 64), strings.NewReader(body), int64(len(body)), hex.EncodeToString(digest[:]), time.Now()); err == nil {
		t.Fatal("oversized leaf accepted")
	}
}
