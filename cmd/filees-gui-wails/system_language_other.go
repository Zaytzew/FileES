//go:build !linux

package main

// Other platforms retain their webview's native OS language integration.
func systemLanguages() []string { return nil }
