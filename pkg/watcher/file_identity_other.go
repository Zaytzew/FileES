//go:build !linux

package watcher

func fileIdentity(string) string { return "" }
