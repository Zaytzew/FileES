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
// "C:" to answer. One rule in one place is the point of this package - and on
// 2026-09-24 five more sites were found building the URL by hand with
// net/url, three of them still without the drive-letter slash (one kept
// pkg/repoworker's ownership test failing on Windows for weeks).
package svnurl

import (
	"net/url"
	"path/filepath"
	"strings"
)

// File returns the file:// URL for a local repository path.
//
// The third slash is what makes it a local path; on POSIX the path already
// begins with one. The path is percent-encoded as a URL path, so a space, "%"
// or "#" in a repositories root cannot break Subversion's URL parsing. A
// trailing "@" peg-revision escape is the caller's, added after this.
func File(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
