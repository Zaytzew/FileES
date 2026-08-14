//go:build !windows

package processoutput

// Unix-facing FileES process protocols are UTF-8. Invalid diagnostic bytes
// are made safe for Go strings, logs and JSON without guessing a locale.
func platformText(data []byte) string { return replacementText(data) }
