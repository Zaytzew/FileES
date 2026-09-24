//go:build windows

// filees-launch starts the MSI supervisor (start-filees.ps1) with no window
// at all. It replaces the wscript shim start-filees.vbs as the target of the
// Start menu, desktop and Startup shortcuts: Microsoft is retiring VBScript,
// and a clean Windows Sandbox on 2026-09-24 already answered the shortcut
// with "There is no script engine for file extension .vbs", so FileES never
// started.
//
// Built with -H=windowsgui, so no console is ever created for it, and the
// PowerShell child gets CREATE_NO_WINDOW - the property the shim existed for:
// powershell.exe -WindowStyle Hidden still flashes a console, because the
// console host exists before the style applies (packaging/windows/AUTOSTART.md).
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

func main() {
	os.Exit(run(os.Args[1:]))
}

// run mirrors autostart-launch.vbs: no argument starts the supervisor, --show
// also asks it to bring up the interface, anything else is refused with 2.
// It does not wait: the supervisor lives for the whole session.
func run(args []string) int {
	command, ok := supervisorCommand(args)
	if !ok {
		return 2
	}
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	here := filepath.Dir(executable)
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		append(command, filepath.Join(here, "start-filees.ps1"))...)
	if len(args) == 1 {
		cmd.Args = append(cmd.Args, "-ShowGUI")
	}
	cmd.Dir = here
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return 1
	}
	_ = cmd.Process.Release()
	return 0
}

// supervisorCommand is the fixed PowerShell prefix, or false for arguments
// the shortcuts never pass.
func supervisorCommand(args []string) ([]string, bool) {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--show") {
		return nil, false
	}
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}, true
}
