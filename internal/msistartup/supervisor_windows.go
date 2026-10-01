//go:build windows

// Package msistartup owns MSI startup without interpreter subprocesses.
package msistartup

import (
	"context"
	"errors"
	"filees/internal/gui/singleinstance"
	"filees/internal/predecessor"
	"filees/pkg/ipcclient"
	"fmt"
	"golang.org/x/sys/windows"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const supervisorID = "FileESDesktopMSISupervisor"

type InstallPaths struct{ Root, Config, Logs, Daemon, GUI, Socket, LockID string }

func Paths(root string) InstallPaths {
	return InstallPaths{root, filepath.Join(root, "config.json"), filepath.Join(root, "logs"), filepath.Join(root, "filees.exe"), filepath.Join(root, "filees-gui-wails.exe"), ipcclient.DefaultSocketPath(), supervisorID}
}
func Note(p InstallPaths, text string) {
	if os.MkdirAll(p.Logs, 0700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(p.Logs, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		defer f.Close()
		log.New(f, "", log.LstdFlags|log.LUTC).Print(text)
	}
}
func hidden(program, root string, args ...string) *exec.Cmd {
	c := exec.Command(program, args...)
	c.Dir = root
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return c
}
func dialog(p InstallPaths, args ...string) error {
	err := hidden(p.GUI, p.Root, args...).Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return ErrCancelled
	}
	return err
}
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func acquire(ctx context.Context, id string) (singleinstance.Lock, error) {
	for {
		lock, err := singleinstance.Acquire(id)
		if !errors.Is(err, singleinstance.ErrAlreadyRunning) {
			return lock, err
		}
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}
func setup(ctx context.Context, p InstallPaths, interactive bool) (bool, error) {
	lock, err := acquire(ctx, "FileESDesktopMSISetup")
	if err != nil {
		return false, err
	}
	defer lock.Close()
	ok, err := ResolveStore(interactive, predecessor.StorePresent, func() error { return dialog(p, "--replace-predecessor", "store", "--config", p.Config) })
	if err != nil || !ok {
		return ok, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	err = EnsureConfig(p.Config, home, func(path string) error { return dialog(p, "--choose-update-channel", path) })
	return err == nil, err
}
func ready(p InstallPaths) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, err := ipcclient.New(p.Socket, "filees-msi-supervisor").Hello(ctx)
	return err == nil
}
func showGUI(p InstallPaths) error {
	c := hidden(p.GUI, p.Root)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// Open performs interactive setup, then waits for our daemon and singleton.
// Seeing a foreign daemon on the shared socket is not successful startup.
func Open(ctx context.Context, p InstallPaths, start func() error) error {
	ok, err := setup(ctx, p, true)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := start(); err != nil {
		return err
	}
	lastStart := time.Now()
	for {
		owned, err := supervisorRunning(p.LockID)
		if err != nil {
			return err
		}
		if !owned && time.Since(lastStart) > time.Second {
			if err := start(); err != nil {
				return err
			}
			lastStart = time.Now()
		}
		proc, err := findDaemon(p.Root)
		if err != nil {
			return err
		}
		if proc != nil {
			proc.Close()
			if owned && ready(p) {
				return showGUI(p)
			}
		}
		if err := pause(ctx, 200*time.Millisecond); err != nil {
			return fmt.Errorf("MSI pair did not become ready; see %s: %w", p.Logs, err)
		}
	}
}

// Inspection must not create a mutex: doing so can make the newly spawned
// supervisor mistake a short-lived reader for an existing supervisor.
func supervisorRunning(id string) (bool, error) {
	name, err := windows.UTF16PtrFromString(`Local\` + id)
	if err != nil {
		return false, err
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	windows.CloseHandle(h)
	return true, nil
}
func startDaemon(p InstallPaths) (*process, error) {
	if err := os.MkdirAll(p.Logs, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(p.Logs, "daemon-"+time.Now().UTC().Format("20060102T150405.000000000")+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := hidden(p.Daemon, p.Root, "daemon", "--config", p.Config)
	c.Stdout = f
	c.Stderr = f
	if err := c.Start(); err != nil {
		return nil, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(c.Process.Pid))
	_ = c.Process.Release()
	if err != nil {
		return nil, err
	}
	return &process{h, uint32(c.Process.Pid)}, nil
}

func Supervise(ctx context.Context, p InstallPaths, interactive bool) error {
	return supervise(ctx, p, interactive, setup, 15*time.Second)
}

func supervise(ctx context.Context, p InstallPaths, interactive bool, initialize func(context.Context, InstallPaths, bool) (bool, error), backoff time.Duration) error {
	lock, err := singleinstance.Acquire(p.LockID)
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	ok, err := initialize(ctx, p, interactive)
	if err != nil || !ok {
		return err
	}
	tracked, err := findDaemon(p.Root)
	if err != nil {
		return err
	}
	if tracked == nil && ready(p) {
		return errors.New("foreign daemon owns user socket; MSI startup refused")
	}
	for tracked == nil {
		tracked, err = startDaemon(p)
		if err != nil {
			Note(p, fmt.Sprintf("daemon start failed: %v", err))
			if err := pause(ctx, backoff); err != nil {
				return err
			}
			if ready(p) {
				return errors.New("foreign daemon owns user socket; start retry refused")
			}
			tracked, err = findDaemon(p.Root)
			if err != nil {
				return err
			}
			continue
		}
		Note(p, "daemon started")
	}
	Note(p, fmt.Sprintf("daemon tracked (pid %d)", tracked.pid))
	defer func() {
		if tracked != nil {
			tracked.Close()
		}
	}()
	if !interactive {
		waitCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		for !ready(p) && waitCtx.Err() == nil {
			_ = pause(waitCtx, 200*time.Millisecond)
		}
		cancel()
		if ready(p) {
			if err := showGUI(p); err != nil {
				Note(p, fmt.Sprintf("GUI: %v", err))
			}
		}
	}
	for {
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		exited, code, err := tracked.Exited()
		if err != nil {
			return err
		}
		if !exited {
			continue
		}
		next, err := findDaemon(p.Root)
		if err != nil {
			return err
		}
		if next != nil {
			tracked.Close()
			tracked = next
			Note(p, "replacement daemon adopted")
			continue
		}
		if code == 0 {
			Note(p, "daemon stopped normally - supervisor exits")
			return nil
		}
		Note(p, fmt.Sprintf("daemon failed (exit %d) - restarting", code))
		for {
			if err := pause(ctx, backoff); err != nil {
				return err
			}
			next, err = findDaemon(p.Root)
			if err != nil {
				return err
			}
			if next == nil && ready(p) {
				return errors.New("foreign daemon owns user socket; restart refused")
			}
			if next == nil {
				next, err = startDaemon(p)
			}
			if err != nil {
				Note(p, fmt.Sprintf("restart failed: %v", err))
				continue
			}
			tracked.Close()
			tracked = next
			break
		}
	}
}
