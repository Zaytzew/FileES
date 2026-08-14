// Package processoutput normalizes text emitted by external programs before
// it crosses into FileES logs, errors, JSON state, or the GUI.
package processoutput

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Text decodes human-facing process output. UTF-8 wins whenever the byte
// stream is already valid; platformText handles a native legacy code page
// only as a fallback.
func Text(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return platformText(data)
}

// UTF8 accepts machine-readable process output only when it is valid UTF-8.
// Protocol data must never be guessed from the workstation's active code
// page, because that can silently change repository paths and identifiers.
func UTF8(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", errors.New("process output is not valid UTF-8")
	}
	return string(data), nil
}

func replacementText(data []byte) string {
	return strings.ToValidUTF8(string(data), "\uFFFD")
}
