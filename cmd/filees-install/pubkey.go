package main

import _ "embed"

//go:embed assets/release.pub
var embeddedPubkey []byte

// pubkeyConfigured returns true if the embedded key looks like a real
// signify public key (not the placeholder shipped in the source tree).
func pubkeyConfigured() bool {
	for _, line := range splitLines(embeddedPubkey) {
		if len(line) > 0 && line[0] != '#' {
			return len(line) > 20 && !containsBytes(line, []byte("xxxx"))
		}
	}
	return false
}

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			lines = append(lines, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
outer:
	for i := 0; i <= len(haystack)-len(needle); i++ {
		for j := range needle {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
