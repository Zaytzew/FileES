//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"filees/internal/gui/actions"

	"golang.org/x/sys/windows"
)

// elevatedAnchorRepairer starts `filees.exe anchor-repair` from this
// installation with the "runas" verb. The consent to elevate is Windows's own
// prompt; this code neither stores nor forwards credentials. The command gets
// one folder and reports with its exit code (cmd/filees/anchor_repair_windows.go).
type elevatedAnchorRepairer struct{ daemon string }

func newAnchorRepairer() actions.AnchorRepairer {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	daemon := filepath.Join(filepath.Dir(exe), "filees.exe")
	if info, err := os.Stat(daemon); err != nil || info.IsDir() {
		return nil
	}
	return elevatedAnchorRepairer{daemon: daemon}
}

type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    windows.Handle
	idList     uintptr
	class      *uint16
	hkeyClass  windows.Handle
	hotKey     uint32
	iconOrMon  windows.Handle
	process    windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	anchorRepairTimeout   = 10 * time.Minute
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// anchorRepairArguments quotes the one argument that is data. A Windows path
// cannot contain a quote; a trailing backslash would escape the closing one.
func anchorRepairArguments(root string, reattachOnly bool) (string, error) {
	if !filepath.IsAbs(root) || strings.ContainsAny(root, "\"\x00\r\n") {
		return "", errors.New("folder path cannot be passed to the repair command")
	}
	arguments := `anchor-repair --root "` + strings.TrimRight(filepath.Clean(root), `\`) + `"`
	if reattachOnly {
		arguments += " --reattach"
	}
	return arguments, nil
}

// anchorRepairOutcome maps the command's exit code.
func anchorRepairOutcome(code uint32) actions.AnchorRepairOutcome {
	switch code {
	case 0:
		return actions.AnchorRepairDone
	case 3:
		return actions.AnchorRepairNoRights
	case 6:
		return actions.AnchorRepairIncomplete
	case 7:
		return actions.AnchorRepairFilterDetached
	default:
		return actions.AnchorRepairFailed
	}
}

func (repairer elevatedAnchorRepairer) RepairAnchor(ctx context.Context, root string, reattachOnly bool) (actions.AnchorRepairOutcome, error) {
	arguments, err := anchorRepairArguments(root, reattachOnly)
	if err != nil {
		return actions.AnchorRepairFailed, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(repairer.daemon)
	if err != nil {
		return actions.AnchorRepairFailed, err
	}
	parameters, err := windows.UTF16PtrFromString(arguments)
	if err != nil {
		return actions.AnchorRepairFailed, err
	}
	directory, _ := windows.UTF16PtrFromString(filepath.Dir(repairer.daemon))
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI, verb: verb, file: file, parameters: parameters, directory: directory, show: windows.SW_HIDE}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		// Declined, cancelled, or no account that may elevate.
		if errors.Is(callErr, windows.ERROR_CANCELLED) || errors.Is(callErr, syscall.ERROR_ACCESS_DENIED) {
			return actions.AnchorRepairNoRights, nil
		}
		return actions.AnchorRepairFailed, callErr
	}
	if info.process == 0 {
		return actions.AnchorRepairFailed, errors.New("repair command did not start")
	}
	defer windows.CloseHandle(info.process)
	deadline := time.Now().Add(anchorRepairTimeout)
	for {
		event, err := windows.WaitForSingleObject(info.process, 250)
		if err != nil {
			return actions.AnchorRepairFailed, err
		}
		if event == windows.WAIT_OBJECT_0 {
			break
		}
		// The command is never killed: stopping it between detaching and
		// attaching the filter is the one thing that must not happen.
		if ctx.Err() != nil || time.Now().After(deadline) {
			return actions.AnchorRepairFailed, errors.New("repair command is still running; its result is unknown")
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return actions.AnchorRepairFailed, err
	}
	return anchorRepairOutcome(code), nil
}
