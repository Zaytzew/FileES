package client

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const nativeCommitTargetLimit = 65536
const nativeCommitTargetBytes = 16 * 1024 * 1024

// Native commit targets use NUL-terminated UTF-8 records on stdin, not argv.
// Validate the entire bounded set before any mutation; never split a commit.
func nativeCommitTargets(paths []string) ([]byte, error) {
	if len(paths) == 0 || len(paths) > nativeCommitTargetLimit {
		return nil, errors.New("native commit requires 1..65536 explicit targets (no split)")
	}
	size := 0
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if !utf8.ValidString(path) || !validMovePath(path) || strings.ContainsFunc(path, func(r rune) bool { return r < 32 || r == 127 }) {
			return nil, errors.New("native commit requires canonical UTF-8 data targets")
		}
		if seen[path] {
			return nil, errors.New("native commit refuses duplicate targets")
		}
		seen[path] = true
		if len(path)+1 > nativeCommitTargetBytes-size {
			return nil, errors.New("native commit target input exceeds 16 MiB (no split)")
		}
		size += len(path) + 1
	}
	input := make([]byte, 0, size)
	for _, path := range paths {
		input = append(input, path...)
		input = append(input, 0)
	}
	return input, nil
}
