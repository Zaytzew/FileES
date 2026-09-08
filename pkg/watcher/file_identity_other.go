//go:build !linux && !windows

package watcher

func fileIdentity(string) string { return "" }
