//go:build windows

// filees-launch is both the MSI shortcut target and its native supervisor.
// Ordinary startup never executes a shell or an interpreted script.
package main

import (
	"context"
	"errors"
	"filees/internal/msistartup"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const createNoWindow = 0x08000000

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	mode, ok := launchMode(args)
	if !ok {
		return 2
	}
	if mode == "prepare-upgrade" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		paths := msistartup.Paths(args[1])
		if err := msistartup.PrepareUpgrade(ctx, paths); err != nil {
			msistartup.Note(paths, fmt.Sprintf("upgrade stop: %v", err))
			return 1
		}
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	paths := msistartup.Paths(filepath.Dir(exe))
	if mode == "supervise" || mode == "interactive-supervise" {
		err = msistartup.Supervise(context.Background(), paths, mode == "interactive-supervise")
	} else if mode == "show" {
		err = msistartup.Open(context.Background(), paths, func() error { return startSupervisor(exe, paths.Root, true) })
	} else {
		err = startSupervisor(exe, paths.Root, false)
	}
	if err != nil {
		msistartup.Note(paths, fmt.Sprintf("launcher: %v", err))
		if mode == "show" && !errors.Is(err, msistartup.ErrCancelled) {
			showError(err.Error())
		}
		return 1
	}
	return 0
}

func startSupervisor(exe, root string, interactive bool) error {
	args := []string{"--supervise"}
	if interactive {
		args = append(args, "--interactive")
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func launchMode(args []string) (string, bool) {
	if len(args) == 0 {
		return "startup", true
	}
	if len(args) == 2 && args[0] == "--prepare-upgrade" && filepath.IsAbs(args[1]) && filepath.Clean(args[1]) != filepath.VolumeName(args[1])+string(os.PathSeparator) {
		return "prepare-upgrade", true
	}
	if len(args) == 2 && args[0] == "--supervise" && args[1] == "--interactive" {
		return "interactive-supervise", true
	}
	if len(args) != 1 {
		return "", false
	}
	switch args[0] {
	case "--show":
		return "show", true
	case "--supervise":
		return "supervise", true
	default:
		return "", false
	}
}

func showError(message string) {
	title, _ := syscall.UTF16PtrFromString("FileES")
	text, err := syscall.UTF16PtrFromString(message)
	if err == nil {
		_, _, _ = syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
	}
}
