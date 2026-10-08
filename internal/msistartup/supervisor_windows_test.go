//go:build windows

package msistartup

import (
	"context"
	"encoding/json"
	"errors"
	"filees/internal/gui/singleinstance"
	contract "filees/pkg/contract/v1"
	"fmt"
	"golang.org/x/sys/windows"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func terminateFixture(h windows.Handle) error { return windows.TerminateProcess(h, 1) }

func TestMain(m *testing.M) {
	if os.Getenv("FILEES_MSI_FIXTURE") == "1" {
		if filepath.Base(os.Args[0]) == "filees-gui-wails.exe" {
			_ = os.WriteFile("gui-called", []byte("yes"), 0600)
			os.Exit(0)
		}
		root, _ := os.Getwd()
		_ = os.WriteFile(filepath.Join(root, "pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
		sock := os.Getenv("FILEES_MSI_TEST_SOCKET")
		_ = os.Remove(sock)
		listener, err := net.Listen("unix", sock)
		if err != nil {
			os.Exit(3)
		}
		go func() {
			for {
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				go func() {
					defer conn.Close()
					var req contract.Request
					if json.NewDecoder(conn).Decode(&req) == nil {
						json.NewEncoder(conn).Encode(contract.Response{Protocol: contract.Protocol, RequestID: req.RequestID, Status: contract.StatusOK, Result: json.RawMessage(`{"version":"fixture"}`)})
						if req.Command == contract.CmdSystemShutdown {
							delay, _ := strconv.Atoi(os.Getenv("FILEES_MSI_TEST_STOP_DELAY_MS"))
							go func() {
								time.Sleep(time.Duration(delay) * time.Millisecond)
								_ = os.WriteFile(filepath.Join(root, "quit"), []byte("yes"), 0600)
							}()
						}
					}
				}()
			}
		}()
		for {
			if _, err := os.Stat(filepath.Join(root, "quit")); err == nil {
				listener.Close()
				os.Remove(sock)
				os.Exit(0)
			}
			if _, err := os.Stat(filepath.Join(root, "crash")); err == nil {
				os.Remove(filepath.Join(root, "crash"))
				listener.Close()
				os.Remove(sock)
				os.Exit(7)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

func startupFixture(t *testing.T) InstallPaths {
	t.Helper()
	p := Paths(t.TempDir())
	short, err := os.MkdirTemp(os.TempDir(), "msi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	p.Socket = filepath.Join(short, "test.sock")
	p.LockID = "FileESMSITest-" + filepath.Base(short)
	t.Setenv("FILEES_MSI_FIXTURE", "1")
	t.Setenv("FILEES_MSI_TEST_SOCKET", p.Socket)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Daemon, p.GUI} {
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		proc, _ := findDaemon(p.Root)
		if proc != nil {
			h := proc.handle
			_ = terminateFixture(h)
			proc.Close()
		}
	})
	return p
}
func initialized(context.Context, InstallPaths, bool) (bool, error) { return true, nil }
func waitUntil(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestNativeSupervisorStartSingletonCrashQuitAndReopen(t *testing.T) {
	p := startupFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- supervise(ctx, p, false, initialized, 50*time.Millisecond) }()
	waitUntil(t, func() bool { _, err := os.Stat(filepath.Join(p.Root, "gui-called")); return err == nil })
	first, err := findDaemon(p.Root)
	if err != nil || first == nil {
		t.Fatal("no owned daemon", err)
	}
	pid := first.pid
	first.Close()
	if err := supervise(ctx, p, true, initialized, 50*time.Millisecond); err != nil {
		t.Fatal("duplicate supervisor", err)
	}
	os.WriteFile(filepath.Join(p.Root, "crash"), []byte("yes"), 0600)
	waitUntil(t, func() bool {
		proc, _ := findDaemon(p.Root)
		if proc == nil {
			return false
		}
		defer proc.Close()
		return proc.pid != pid && ready(p)
	})
	os.WriteFile(filepath.Join(p.Root, "quit"), []byte("yes"), 0600)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normal quit did not stop supervisor")
	}
	lock, err := singleinstance.Acquire(p.LockID)
	if err != nil {
		t.Fatal("singleton leaked", err)
	}
	lock.Close()
	os.Remove(filepath.Join(p.Root, "quit"))
	done = make(chan error, 1)
	go func() { done <- supervise(ctx, p, true, initialized, 50*time.Millisecond) }()
	waitUntil(t, func() bool { return ready(p) })
	os.WriteFile(filepath.Join(p.Root, "quit"), []byte("yes"), 0600)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reopened pair did not quit")
	}
}

func TestNativeSupervisorAdoptsOwnedDaemonAndCancelledSetupStartsNothing(t *testing.T) {
	p := startupFixture(t)
	if err := supervise(t.Context(), p, true, func(context.Context, InstallPaths, bool) (bool, error) { return false, ErrCancelled }, time.Millisecond); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Root, "pid")); !os.IsNotExist(err) {
		t.Fatal("daemon started after cancellation")
	}
	proc, err := startDaemon(p)
	if err != nil {
		t.Fatal(err)
	}
	pid := proc.pid
	proc.Close()
	waitUntil(t, func() bool { return ready(p) })
	done := make(chan error, 1)
	go func() { done <- supervise(t.Context(), p, true, initialized, time.Millisecond) }()
	waitUntil(t, func() bool { raw, _ := os.ReadFile(filepath.Join(p.Logs, "supervisor.log")); return string(raw) != "" })
	adopted, err := findDaemon(p.Root)
	if err != nil || adopted == nil {
		t.Fatal(err)
	}
	if adopted.pid != pid {
		t.Fatal("started another daemon")
	}
	adopted.Close()
	os.WriteFile(filepath.Join(p.Root, "quit"), []byte("yes"), 0600)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("adopted daemon not observed")
	}
}

func TestNativeSupervisorHandsRunningPairToUpdatedLauncher(t *testing.T) {
	p := startupFixture(t)
	launcher := filepath.Join(p.Root, "filees-launch.exe")
	if err := os.WriteFile(launcher, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	loaded, err := os.Stat(launcher)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	successorDone := make(chan error, 1)
	old := &Image{Path: launcher, Loaded: loaded, Start: func() error {
		current, err := os.Stat(launcher)
		if err != nil {
			return err
		}
		next := &Image{Path: launcher, Loaded: current, Successor: true, Start: func() error { return errors.New("second handover") }}
		go func() { successorDone <- superviseImage(ctx, p, false, initialized, 50*time.Millisecond, next) }()
		return nil
	}}
	oldDone := make(chan error, 1)
	go func() { oldDone <- superviseImage(ctx, p, true, initialized, 50*time.Millisecond, old) }()
	waitUntil(t, func() bool { return ready(p) })
	first, err := findDaemon(p.Root)
	if err != nil || first == nil {
		t.Fatal("no owned daemon", err)
	}
	pid := first.pid
	first.Close()
	select {
	case err := <-oldDone:
		t.Fatal("supervisor left without an update", err)
	case <-time.After(1500 * time.Millisecond):
	}

	// What the channel update does: the running image is moved aside.
	if err := os.Rename(launcher, launcher+".superseded-test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old supervisor did not hand over")
	}
	waitUntil(t, func() bool {
		lock, err := singleinstance.Acquire(p.LockID)
		if err == nil {
			lock.Close()
		}
		return errors.Is(err, singleinstance.ErrAlreadyRunning)
	})
	proc, err := findDaemon(p.Root)
	if err != nil || proc == nil {
		t.Fatal("pair did not survive the handover", err)
	}
	if proc.pid != pid {
		t.Fatalf("daemon restarted during handover: %d -> %d", pid, proc.pid)
	}
	proc.Close()
	if _, err := os.Stat(filepath.Join(p.Root, "gui-called")); err == nil {
		t.Fatal("successor opened the GUI")
	}
	os.WriteFile(filepath.Join(p.Root, "quit"), []byte("yes"), 0600)
	select {
	case err := <-successorDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("successor did not follow the daemon's normal stop")
	}
}
