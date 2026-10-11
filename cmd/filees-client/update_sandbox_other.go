//go:build !openbsd

package main

func lockInstallSandbox(_ []string, _, _ string) error { return nil }
