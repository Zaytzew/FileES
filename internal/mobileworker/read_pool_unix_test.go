//go:build linux || openbsd

package mobileworker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "filees/pkg/mobile/v1"
	"filees/public-shares/storage"
	"github.com/google/uuid"
)

type readPoolSendProbe func([]byte) (int, error)

func (f readPoolSendProbe) Write(p []byte) (int, error) { return f(p) }

func TestReadPoolLeaseCoversResponseSend(t *testing.T) {
	base := t.TempDir()
	d := newDispatcher(t, "unused", "r")
	d.Appender.SpoolDir, d.MaxReadSpoolBytes = base, 4
	d.Browser.Reader = &readBudgetProbe{t: t, size: 4, data: "data"}
	d.readSpace = func(string, int64) error { return nil }
	checks := 0
	out := readPoolSendProbe(func(p []byte) (int, error) {
		checks++
		err := poolSpool(base, 4).reserve(context.Background(), 1)
		if err == nil || !strings.Contains(err.Error(), "budget exhausted") {
			t.Fatalf("lease ended before response: %v", err)
		}
		return len(p), nil
	})
	frame := frameRequest(t, uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: "r", Path: "file"}, nil)
	if err := d.Serve(context.Background(), bytes.NewReader(frame), out); err != nil {
		t.Fatal(err)
	}
	if checks == 0 {
		t.Fatal("no response")
	}
	assertReadSpoolIdle(t, base)
}

func TestReadPoolCapacityIncludesActiveReservations(t *testing.T) {
	base := t.TempDir()
	a := poolSpool(base, 0)
	if err := a.reserve(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b := poolSpool(base, 0)
	called := false
	b.checkSpace = func(_ string, n int64) error {
		called = true
		if n != 6 {
			t.Fatalf("capacity charge %d, want 6", n)
		}
		return errors.New("no capacity")
	}
	if err := b.reserve(context.Background(), 2); err == nil || !called {
		t.Fatalf("capacity refusal %v, called=%v", err, called)
	}
}

func poolSpool(base string, max int64) *readSpool {
	return &readSpool{root: base, maxBytes: max, checkSpace: func(string, int64) error { return nil }}
}

func TestReadPoolActiveReservationAndUnknownData(t *testing.T) {
	base := t.TempDir()
	a := poolSpool(base, 5)
	if err := a.reserve(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(a.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	b := poolSpool(base, 5)
	if err := b.reserve(context.Background(), 1); err == nil {
		b.Close()
		t.Fatal("active reservation reclaimed or not counted")
	}
	if raw, err := os.ReadFile(a.Name()); err != nil || string(raw) != "abc" {
		t.Fatalf("active data lost: %q %v", raw, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.reserve(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	assertReadSpoolIdle(t, base)
	pool := filepath.Join(base, readPoolName)
	unknown := filepath.Join(pool, "do-not-delete")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := poolSpool(base, 0).reserve(context.Background(), 1); err == nil {
		t.Fatal("unknown pool state accepted")
	}
	if raw, err := os.ReadFile(unknown); err != nil || string(raw) != "keep" {
		t.Fatal("unknown data removed")
	}
}

func TestReadPoolRejectsSymlinksAndSweepsPartialCreation(t *testing.T) {
	base := t.TempDir()
	pool := filepath.Join(base, readPoolName)
	dir := filepath.Join(pool, "read-"+uuid.NewString())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("abandoned"), 0600); err != nil {
		t.Fatal(err)
	}
	s := poolSpool(base, 1)
	if err := s.reserve(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("partial creation retained")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(pool, "read-"+uuid.NewString())
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "payload")); err != nil {
		t.Fatal(err)
	}
	if err := poolSpool(base, 1).reserve(context.Background(), 1); err == nil {
		t.Fatal("symlink accepted")
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatal("symlink target changed")
	}
}

func TestReadPoolProcessProbe(t *testing.T) {
	base := os.Getenv("FILEES_READ_POOL_PROBE")
	if base == "" {
		return
	}
	s := poolSpool(base, 3)
	if err := s.reserve(context.Background(), 1); err != nil {
		fmt.Println("refused")
		return
	}
	defer s.Close()
	if _, err := s.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	fmt.Println("accepted")
	_, _ = io.Copy(io.Discard, os.Stdin) // keep lease through simulated slow send
}

func TestReadPoolConcurrentProcessesAndKilledWriter(t *testing.T) {
	base := t.TempDir()
	var commands []*exec.Cmd
	var inputs []io.WriteCloser
	var outputs []*bufio.Reader
	for i := 0; i < 8; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReadPoolProcessProbe$")
		cmd.Env = append(os.Environ(), "FILEES_READ_POOL_PROBE="+base)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			in.Close()
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		commands = append(commands, cmd)
		inputs = append(inputs, in)
		outputs = append(outputs, bufio.NewReader(out))
	}
	var accepted []int
	for i, out := range outputs {
		line, err := out.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "accepted\n" {
			accepted = append(accepted, i)
		} else if line != "refused\n" {
			t.Fatal(line)
		}
	}
	if len(accepted) != 3 {
		t.Fatalf("accepted %d, want 3", len(accepted))
	}
	if err := poolSpool(base, 3).reserve(context.Background(), 1); err == nil {
		t.Fatal("overcommitted")
	}
	dead := accepted[0]
	if err := commands[dead].Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = commands[dead].Wait()
	replacement := poolSpool(base, 3)
	if err := replacement.reserve(context.Background(), 1); err != nil {
		t.Fatalf("crash did not release budget: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range commands {
		inputs[i].Close()
		if i != dead {
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		}
	}
	last := poolSpool(base, 3)
	if err := last.reserve(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if err := last.Close(); err != nil {
		t.Fatal(err)
	}
	assertReadSpoolIdle(t, base)
}

func TestReadPoolMalformedActiveReservationFailsClosed(t *testing.T) {
	base := t.TempDir()
	s := poolSpool(base, 5)
	if err := s.reserve(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.Name()), "reserved"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := poolSpool(base, 5).reserve(context.Background(), 1); err == nil {
		t.Fatal("malformed active reservation treated as free")
	}
	// Ensure test uses the same owner primitive as the real service.
	if owner, err := storage.Own(filepath.Dir(s.Name())); err == nil {
		owner.Close()
		t.Fatal("lease not held")
	}
}
