//go:build !windows

package main

// allowForegroundHandover has no equivalent outside Windows, where raising a
// window is not gated on already owning the foreground.
func allowForegroundHandover() {}
