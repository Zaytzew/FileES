package svnurl

import (
	"runtime"
	"testing"
)

// One rule for every platform: three slashes before a local path, the drive
// letter part of the path and never the host, and URL escaping for the
// characters that would otherwise break Subversion's URL parsing.
func TestFileURLs(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/var/filees/repositories/abc", "file:///var/filees/repositories/abc"},
		{"C:/Program Files/FileES/repo", "file:///C:/Program%20Files/FileES/repo"},
		{"/srv/100% done/#1", "file:///srv/100%25%20done/%231"},
	}
	if runtime.GOOS == "windows" {
		// A backslash separates path elements only on Windows.
		cases = append(cases, struct{ path, want string }{`C:\Users\acme\repos\abc`, "file:///C:/Users/acme/repos/abc"})
	}
	for _, tc := range cases {
		if got := File(tc.path); got != tc.want {
			t.Errorf("File(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
