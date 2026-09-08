// Package svnurl builds repository URLs for local paths.
//
// It exists because "file://" + path is wrong on Windows and right on POSIX,
// so the mistake is invisible to whoever writes it and fatal to whoever runs
// it elsewhere. A Windows path yields file://C:/Users/... , where C: is read
// as the HOST: Subversion then reports either "Illegal repository URL" or a
// connection it cannot make, and the test that built the URL looks like a
// product failure.
//
// Measured 2026-09-08: six packages carried the same defect independently,
// and three of them spent 53, 45 and 29 seconds each waiting for a host named
// "C:" to answer. One rule in one place is the point of this package.
package svnurl

import (
	"path/filepath"
	"strings"
)

// File returns the file:// URL for a local repository path.
//
// The third slash is what makes it a local path. On POSIX the path already
// begins with one, so this is identity there.
func File(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}
