package main

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"filees/public-shares/cache"
	"filees/public-shares/linkservice"
	"filees/public-shares/storage"
)

func TestLinksRuntimeMaintenanceAndExclusiveStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server lifecycle uses Unix signals")
	}
	if path := os.Getenv("FILEES_M48_LINKS_CONFIG"); path != "" {
		if os.Getenv("FILEES_M48_LINKS_CHECK") == "1" {
			if err := checkMaintenance(path); err != nil {
				t.Fatal(err)
			}
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := run(ctx, path); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "cache")
	if err := os.Mkdir(cacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "visit.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))), 0600); err != nil {
		t.Fatal(err)
	}
	config := linkservice.Config{Schema: linkservice.ConfigSchema, VisitKeyFile: key,
		FastCGI:     linkservice.FastCGIEndpoint{Endpoint: linkservice.Endpoint{Network: "unix", Address: filepath.Join(root, "links.sock")}},
		Backchannel: linkservice.Endpoint{Network: "unix", Address: filepath.Join(root, "authority.sock")},
		Cache:       linkservice.CacheConfig{Enabled: true, Root: cacheRoot, TTL: "1s", MaxSize: 1024, CleanupInterval: "5m"}}
	raw, _ := json.Marshal(config)
	path := filepath.Join(root, "links.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := &cache.Store{Config: cache.Config{Root: cacheRoot, TTL: time.Second, MaxSize: 1024}}
	body := "old bytes"
	sum := md5.Sum([]byte(body))
	if err := store.Put(strings.Repeat("a", 64), strings.NewReader(body), int64(len(body)), hex.EncodeToString(sum[:]), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := func(check bool) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLinksRuntimeMaintenanceAndExclusiveStartup$")
		cmd.Env = append(os.Environ(), "FILEES_M48_LINKS_CONFIG="+path)
		if check {
			cmd.Env = append(cmd.Env, "FILEES_M48_LINKS_CHECK=1")
		}
		return cmd
	}
	command := child(false)
	command.Stderr = os.Stderr
	command.Stdout = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := storage.CheckMaintenance(cacheRoot, 5*time.Minute, time.Now())
		if err == nil {
			if status.Entries != 1 {
				t.Fatalf("startup sweep=%+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime startup timeout", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if output, err := child(false).CombinedOutput(); err == nil || !strings.Contains(string(output), "already owned") {
		t.Fatalf("second instance=%v %s", err, output)
	}
	if output, err := child(true).CombinedOutput(); err != nil || !strings.Contains(string(output), `"state":"checked"`) {
		t.Fatalf("read-only check=%v %s", err, output)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CheckMaintenance(cacheRoot, 5*time.Minute, time.Now()); err == nil {
		t.Fatal("stopped service healthy")
	}
	owner, err := storage.Own(cacheRoot)
	if err != nil {
		t.Fatal("owner not released", err)
	}
	owner.Close()
}
