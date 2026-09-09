//go:build !windows

package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerExcludesAnotherInstanceAndRecoversAfterCrash(t *testing.T) {
	if os.Getenv("FILEES_M48_OWNER_CHILD") == "1" {
		root := os.Getenv("FILEES_M48_ROOT")
		owner, err := Own(root)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		staging := &Staging{Root: root}
		file, _, err := staging.Create()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("crash bytes"); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		os.Stdout.WriteString("ready\n")
		for {
			time.Sleep(time.Hour)
		}
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestOwnerExcludesAnotherInstanceAndRecoversAfterCrash$")
	command.Env = append(os.Environ(), "FILEES_M48_OWNER_CHILD=1", "FILEES_M48_ROOT="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	ready := make(chan string, 1)
	reader := bufio.NewReader(stdout)
	go func() { line, _ := reader.ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			rest, _ := io.ReadAll(reader)
			t.Fatalf("child=%q %s", line, rest)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child timeout")
	}
	if owner, err := Own(root); err == nil {
		owner.Close()
		t.Fatal("second owner admitted")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	owner, err := Own(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	result, err := (&Staging{Root: root}).Sweep(context.Background(), time.Now())
	if err != nil || result.Files != 1 || result.Bytes != 11 {
		t.Fatalf("crash cleanup=%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".maintenance.lock")); err != nil {
		t.Fatal("lock inode removed")
	}
}

func TestStagingKeepsActiveAndUnknownFiles(t *testing.T) {
	root := t.TempDir()
	staging := &Staging{Root: root}
	f, release, err := staging.Create()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer release()
	unknown := filepath.Join(root, "notes.txt")
	os.WriteFile(unknown, []byte("keep"), 0600)
	result, err := staging.Sweep(context.Background(), time.Now())
	if err != nil || result.Files != 0 || result.Active != 1 {
		t.Fatalf("active=%+v %v", result, err)
	}
	f.Close()
	release()
	result, err = staging.Sweep(context.Background(), time.Now())
	if err != nil || result.Files != 1 {
		t.Fatalf("orphan=%+v %v", result, err)
	}
	if raw, _ := os.ReadFile(unknown); string(raw) != "keep" {
		t.Fatal("unknown removed")
	}
}

func TestMaintenanceRunsWithoutTrafficAndRecordsFailureRecoveryStop(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	m := &Maintenance{Root: root, Interval: 20 * time.Millisecond, Sweep: func(context.Context, time.Time) (SweepResult, error) {
		calls.Add(1)
		if fail.Load() {
			return SweepResult{}, errors.New("test removal failure")
		}
		return SweepResult{Files: 2, Bytes: 42}, nil
	}}
	stop := m.Start(context.Background())
	defer stop()
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("maintenance timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(func() bool {
		raw, _ := os.ReadFile(filepath.Join(root, maintenanceStatus))
		return strings.Contains(string(raw), "test removal failure")
	})
	if _, err := CheckMaintenance(root, time.Second, time.Now()); err == nil {
		t.Fatal("failed status reported healthy")
	}
	fail.Store(false)
	wait(func() bool {
		status, err := CheckMaintenance(root, time.Second, time.Now())
		return err == nil && status.Files == 2 && status.Bytes == 42
	})
	if calls.Load() < 2 {
		t.Fatal("no periodic recovery pass")
	}
	stop()
	if _, err := CheckMaintenance(root, time.Second, time.Now()); err == nil {
		t.Fatal("stopped status reported healthy")
	}
}

func TestMaintenanceHealthRejectsMissingStaleFutureRunningAndTrailing(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	if _, err := CheckMaintenance(root, time.Second, now); err == nil {
		t.Fatal("missing accepted")
	}
	good := MaintenanceStatus{Schema: "filees.public-storage-maintenance/v1", State: "checked", StartedAt: now.Add(-time.Second), FinishedAt: now, LastSuccess: now}
	for _, kind := range []string{"good", "stale", "future", "running", "tail"} {
		t.Run(kind, func(t *testing.T) {
			status := good
			at := now
			switch kind {
			case "stale":
				at = now.Add(3 * time.Second)
			case "future":
				at = now.Add(-time.Second)
			case "running":
				status.State = "running"
			}
			raw, _ := json.Marshal(status)
			if kind == "tail" {
				raw = append(raw, []byte(" {}")...)
			}
			if err := os.WriteFile(filepath.Join(root, maintenanceStatus), raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := CheckMaintenance(root, time.Second, at)
			if (err == nil) != (kind == "good") {
				t.Fatalf("health=%v", err)
			}
		})
	}
}

func TestMaintenanceRejectsUnsafeRootsAndKeepsSymlinkTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions/symlinks")
	}
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if owner, err := Own(alias); err == nil {
		owner.Close()
		t.Fatal("symlink root accepted")
	}
	os.Chmod(root, 0755)
	if owner, err := Own(root); err == nil {
		owner.Close()
		t.Fatal("public root accepted")
	}
	os.Chmod(root, 0700)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("keep"), 0600)
	os.Symlink(outside, filepath.Join(root, ".public-share-leaf-123.tmp"))
	if _, err := (&Staging{Root: root}).Sweep(context.Background(), time.Now()); err == nil {
		t.Fatal("symlink not reported")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "keep" {
		t.Fatal("outside changed")
	}
}

func TestCleanupIntervalValidation(t *testing.T) {
	for _, value := range []string{"", "1s", "5m", "1h"} {
		if _, err := CleanupInterval(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"0", "0s", "-1s", "1ms", "2h", "garbage"} {
		if _, err := CleanupInterval(value); err == nil {
			t.Fatal("accepted", value)
		}
	}
}
