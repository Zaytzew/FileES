//go:build windows

// The Store launcher is deliberately separate from the MSI supervisor. MSIX
// payloads are immutable, so both working directory and logs live in the user
// profile. The same source is linked in two modes: interactive and startup.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"filees/internal/gui/singleinstance"
	"filees/pkg/ipcclient"
)

// A Store build must set this to interactive or startup with -ldflags -X.
// An unstamped build fails closed instead of starting a second daemon.
var launcherMode string

const createNoWindow = 0x08000000

type storePaths struct {
	home       string
	root       string
	config     string
	logs       string
	daemon     string
	gui        string
	supervisor string
}

func pathsFor(home, installDir string) storePaths {
	root := filepath.Join(home, ".filees", "store")
	return storePaths{
		home: home, root: root, config: filepath.Join(root, "config.json"),
		logs:       filepath.Join(root, "logs"),
		daemon:     filepath.Join(installDir, "filees.exe"),
		gui:        filepath.Join(installDir, "filees-gui-wails.exe"),
		supervisor: filepath.Join(installDir, "filees-store-startup.exe"),
	}
}

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}
	if launcherMode == "interactive" {
		showError("FileES Desktop could not start", err.Error())
	}
	fmt.Fprintln(os.Stderr, "filees store launcher:", err)
	os.Exit(1)
}

func run(args []string) error {
	if launcherMode != "interactive" && launcherMode != "startup" {
		return fmt.Errorf("invalid Store launcher mode %q", launcherMode)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate user profile: %w", err)
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("user profile path is not absolute: %q", home)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate Store launcher: %w", err)
	}
	paths := pathsFor(home, filepath.Dir(executable))
	if err := refuseLegacyMSI(os.Getenv("LOCALAPPDATA")); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.root, 0o700); err != nil {
		return fmt.Errorf("prepare Store state outside package: %w", err)
	}
	if launcherMode == "interactive" {
		if len(args) != 0 {
			return errors.New("interactive launcher accepts no arguments")
		}
		return runInteractive(paths)
	}
	if len(args) > 1 || len(args) == 1 && args[0] != "--no-gui" {
		return errors.New("startup launcher accepts only --no-gui")
	}
	return runSupervisor(paths, len(args) == 0)
}

func refuseLegacyMSI(localAppData string) error {
	if !filepath.IsAbs(localAppData) {
		return errors.New("LOCALAPPDATA is missing or not absolute; cannot exclude a parallel MSI installation")
	}
	legacy := filepath.Join(localAppData, "Programs", "FileES", "filees.exe")
	_, err := os.Stat(legacy)
	if err == nil {
		return fmt.Errorf("the existing FileES MSI at %s must be migrated and removed before running the Store version", legacy)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing FileES MSI: %w", err)
	}
	return nil
}

func runInteractive(paths storePaths) error {
	owned, err := supervisorRunning()
	if err != nil {
		return err
	}
	if !owned && daemonReady() {
		return errors.New("another FileES daemon is already running without the Store supervisor; close it before starting the Store version")
	}
	if err := startProcess(paths.supervisor, paths.root, nil, "--no-gui"); err != nil {
		return fmt.Errorf("start Store daemon supervisor: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := waitForDaemon(ctx); err != nil {
		return fmt.Errorf("Store daemon did not become ready (see %s): %w", paths.logs, err)
	}
	owned, err = supervisorRunning()
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("a daemon answered, but the Store supervisor is not running; refusing to attach the GUI")
	}
	return startLoggedProcess(paths.gui, paths.root, paths.logs, "gui")
}

func supervisorRunning() (bool, error) {
	lock, err := singleinstance.Acquire("FileESDesktopStoreSupervisor")
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Store supervisor ownership: %w", err)
	}
	if err := lock.Close(); err != nil {
		return false, err
	}
	return false, nil
}

func runSupervisor(paths storePaths, showGUI bool) error {
	lock, err := singleinstance.Acquire("FileESDesktopStoreSupervisor")
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("acquire Store supervisor ownership: %w", err)
	}
	defer lock.Close()
	if err := os.MkdirAll(paths.logs, 0o700); err != nil {
		return fmt.Errorf("prepare Store logs: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(paths.logs, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open Store supervisor log: %w", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags|log.LUTC)
	if err := ensureConfig(paths.config, paths.home); err != nil {
		logger.Printf("initial configuration: %v", err)
		return err
	}
	if daemonReady() {
		err := errors.New("a FileES daemon already owns the user socket; refusing a second daemon")
		logger.Print(err)
		return err
	}
	firstStart := true
	for {
		cmd, err := startDaemon(paths)
		if err != nil {
			logger.Printf("daemon start failed: %v", err)
		} else {
			logger.Printf("daemon started (pid %d)", cmd.Process.Pid)
			if firstStart && showGUI {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				readyErr := waitForDaemon(ctx)
				cancel()
				if readyErr == nil {
					if guiErr := startLoggedProcess(paths.gui, paths.root, paths.logs, "gui"); guiErr != nil {
						logger.Printf("GUI start failed: %v", guiErr)
					}
				} else {
					logger.Printf("GUI not started because daemon is not ready: %v", readyErr)
				}
			}
			if waitErr := cmd.Wait(); waitErr != nil {
				logger.Printf("daemon exited: %v", waitErr)
			} else {
				logger.Print("daemon exited normally")
			}
		}
		firstStart = false
		time.Sleep(15 * time.Second)
		if daemonReady() {
			err := errors.New("another FileES daemon owns the socket after our child exited; refusing restart")
			logger.Print(err)
			return err
		}
	}
}

func ensureConfig(path, home string) error {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return nil
	} else if err == nil {
		return fmt.Errorf("Store configuration path is a directory: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state := filepath.Join(home, ".local", "share", "filees")
	seed := map[string]any{
		"transport": map[string]string{
			"identity_file": filepath.Join(state, "identity", "id_ed25519"),
			"known_hosts":   filepath.Join(state, "known_hosts"),
		},
		"repositories": []any{},
	}
	data, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func startDaemon(paths storePaths) (*exec.Cmd, error) {
	if err := os.MkdirAll(paths.logs, 0o700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(paths.logs, "daemon-"+time.Now().UTC().Format("20060102T150405.000000000")+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	cmd := exec.Command(paths.daemon, "daemon", "--config", paths.config)
	cmd.Dir = paths.root
	cmd.Stdout, cmd.Stderr = file, file
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func startLoggedProcess(program, dir, logs, label string) error {
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(logs, label+"-"+time.Now().UTC().Format("20060102T150405.000000000")+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return startProcess(program, dir, file)
}

func startProcess(program, dir string, output *os.File, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Dir = dir
	if output != nil {
		cmd.Stdout, cmd.Stderr = output, output
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func daemonReady() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, err := ipcclient.New(ipcclient.DefaultSocketPath(), "filees-store-launcher").RepoList(ctx)
	return err == nil
}

func waitForDaemon(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if daemonReady() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

var messageBox = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")

func showError(title, message string) {
	titlePtr, titleErr := syscall.UTF16PtrFromString(title)
	messagePtr, messageErr := syscall.UTF16PtrFromString(message)
	if titleErr == nil && messageErr == nil {
		_, _, _ = messageBox.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), 0x10)
	}
}
