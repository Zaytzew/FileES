//go:build windows

package main

import "golang.org/x/sys/windows"

// allowForegroundHandover lets the running client come forward when this one steps aside.
//
// Windows refuses SetForegroundWindow to a process that does not already own the
// foreground, and answers by flashing the taskbar button instead. Measured: a
// second launch restored the minimised panel correctly and left it behind the
// window the person was looking at - restored, and still not in front of them.
//
// The permission has to be given by the process that has it. A launch from the
// Start Menu or the desktop is that process for a moment, so every start grants
// it before the single-instance lock decides which of the two this is; by the
// time the answer is known, application.New has already notified the other one.
func allowForegroundHandover() {
	// ASFW_ANY. The grant is this process saying it will not hold the
	// foreground against anyone, and this process is about to exit.
	const asfwAny = ^uintptr(0)
	_, _, _ = procAllowSetForegroundWindow.Call(asfwAny)
}

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")
