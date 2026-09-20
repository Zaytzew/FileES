package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"filees/internal/obsandbox"
)

const rotatePromises = "stdio rpath wpath cpath fattr flock proc exec"

// This compatibility entrypoint is also callable directly by cron. It has no
// servertool parent to establish its confinement. The repo parent is writable
// because interrupted swaps must recover even when the hot path is absent.
func confineRotation(repo, archive string) error {
	if runtime.GOOS != "openbsd" {
		return nil
	}
	if !filepath.IsAbs(repo) || !filepath.IsAbs(archive) || filepath.Dir(filepath.Clean(repo)) == string(filepath.Separator) || filepath.Clean(archive) == string(filepath.Separator) {
		return fmt.Errorf("rotation sandbox requires absolute, non-root repository parent and archive")
	}
	if err := obsandbox.Begin(rotatePromises); err != nil {
		return err
	}
	if err := os.MkdirAll(archive, 0750); err != nil {
		return err
	}
	p := obsandbox.Profile{Name: "filees-rotate", Promises: rotatePromises, Paths: []obsandbox.Path{
		{Label: "repo-parent", Name: filepath.Dir(filepath.Clean(repo)), Perms: "rwc"},
		{Label: "archive", Name: filepath.Clean(archive), Perms: "rwc"},
		{Label: "temporary", Name: os.TempDir(), Perms: "rwc"},
		{Label: "null", Name: "/dev/null", Perms: "rw"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "svn-config", Name: "/etc/subversion", Perms: "r"},
	}}
	for _, name := range []string{"svn", "svnadmin", "svnlook"} {
		path, err := exec.LookPath(name)
		if err != nil {
			return err
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		p.Paths = append(p.Paths, obsandbox.Path{Label: name, Name: absolute, Perms: "rx"})
	}
	return obsandbox.ApplyForExec(p, rotatePromises+" prot_exec unveil")
}
