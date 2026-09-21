//go:build !openbsd

package main

// Windows has no pledge/unveil. The OpenBSD profile is the guard that stops
// this one-shot publisher from seeing any working copy except --wc.
func lockPublishSandbox(Config) error { return nil }

func lockActivationSandbox(string, string) error { return nil }
