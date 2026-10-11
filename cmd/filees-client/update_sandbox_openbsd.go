//go:build openbsd

package main

import (
	"os"

	"filees/internal/obsandbox"
)

func lockInstallSandbox(dirs []string, state, svn string) error {
	// SVN's own config/cache/temp lives in private updater state. No
	// activation keys, sync config, working copies or global HOME are unveiled.
	for _, name := range []string{"HOME", "TMPDIR"} {
		if err := os.Setenv(name, state); err != nil {
			return err
		}
	}
	paths := []obsandbox.Path{
		{Label: "svn", Name: svn, Perms: "rx"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "null", Name: "/dev/null", Perms: "rw"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
		{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
		{Label: "tls", Name: "/etc/ssl", Perms: "r"},
		{Label: "locale", Name: "/usr/share/locale/UTF-8/LC_CTYPE", Perms: "r"},
	}
	if info, err := os.Stat("/etc/subversion"); err == nil && info.IsDir() {
		paths = append(paths, obsandbox.Path{Label: "svn-config", Name: "/etc/subversion", Perms: "r"})
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err == nil {
			paths = append(paths, obsandbox.Path{Label: "install", Name: dir, Perms: "rwc"})
		}
	}
	return obsandbox.ApplyForExec(obsandbox.Profile{Name: "filees-client/install", Promises: publishPromises, Paths: paths}, publishChildPromises)
}
