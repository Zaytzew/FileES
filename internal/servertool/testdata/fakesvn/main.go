// Command fakesvn is a native sandbox probe for the S3/S4 worker tests.
// Fedora integration uses real Subversion; this helper implements only the
// four client verbs needed to exercise pledge/unveil in the OpenBSD lab.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	wcMarker        = ".filees-fake-svn-wc"
	publishedMarker = ".filees-fake-svn-published"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "add":
		return
	case "status":
		root, err := findWorkingCopy(lastArgument())
		if err != nil {
			fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, publishedMarker)); errors.Is(err, os.ErrNotExist) {
			fmt.Println("A       activation")
		} else if err != nil {
			fatal(err)
		}
	case "commit":
		root, err := findWorkingCopy(lastArgument())
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, publishedMarker), []byte("2\n"), 0o600); err != nil {
			fatal(err)
		}
	case "info":
		root, err := findWorkingCopy(lastArgument())
		if err != nil {
			fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, publishedMarker)); err != nil {
			fatal(err)
		}
		fmt.Println("2")
	default:
		os.Exit(2)
	}
}

func lastArgument() string {
	if len(os.Args) == 0 {
		return ""
	}
	return os.Args[len(os.Args)-1]
}

func findWorkingCopy(path string) (string, error) {
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		path = filepath.Dir(path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(path, wcMarker)); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", errors.New("fake SVN working copy marker not found")
		}
		path = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
