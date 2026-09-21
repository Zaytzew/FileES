package main

import (
	"errors"
	"os"
	"path/filepath"

	"filees/internal/obsandbox"
)

// publishPromises is ksefpuck's pledge (stdio rpath wpath cpath fattr flock)
// plus the minimum needed to exec svn and ssh. ksefpuck itself never execs
// and has no network. This process does, or it cannot publish the copy.
const publishPromises = "stdio rpath wpath cpath fattr flock inet dns proc exec"

// publishChildPromises is what svn, and the ssh it execs, keep after exec.
// prot_exec is the dynamic linker; filees-client-entry grants the same to svn.
const publishChildPromises = publishPromises + " prot_exec"

const activationPromises = "stdio rpath wpath cpath fattr flock inet dns"

// publishProfile unveils the configured svn and ssh binaries, never PATH,
// and only the working copies the config allows. A --wc outside that set
// is rejected before this profile is applied.
func publishProfile(cfg Config) (obsandbox.Profile, error) {
	if err := cfg.validate(); err != nil {
		return obsandbox.Profile{}, err
	}
	paths := []obsandbox.Path{
		{Label: "identity", Name: cfg.Identity, Perms: "r"},
		{Label: "known-hosts", Name: cfg.KnownHosts, Perms: "r"},
		{Label: "svn", Name: cfg.SVN, Perms: "rx"},
		{Label: "ssh", Name: cfg.SSH, Perms: "rx"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "null-device", Name: "/dev/null", Perms: "rw"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
		{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
		{Label: "tls", Name: "/etc/ssl", Perms: "r"},
		{Label: "ssh-config", Name: "/etc/ssh", Perms: "r"},
		{Label: "svn-temp", Name: "/tmp", Perms: "rwc"},
	}
	for id, spec := range cfg.allowed() {
		paths = append(paths, obsandbox.Path{Label: "wc-" + id, Name: spec.Path, Perms: "rwc"})
	}
	if st, err := os.Stat("/etc/subversion"); err == nil && st.IsDir() {
		paths = append(paths, obsandbox.Path{Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"})
	}
	return obsandbox.Profile{Name: "filees-client/publish", Promises: publishPromises, Paths: paths}, nil
}

func activationProfile(stateRoot, knownHosts string) (obsandbox.Profile, error) {
	if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(knownHosts) {
		return obsandbox.Profile{}, errors.New("activation sandbox requires absolute state root and known_hosts")
	}
	return obsandbox.Profile{Name: "filees-client/activate", Promises: activationPromises, Paths: []obsandbox.Path{
		{Label: "state", Name: stateRoot, Perms: "rwc"},
		{Label: "known-hosts", Name: knownHosts, Perms: "r"},
		{Label: "null-device", Name: "/dev/null", Perms: "rw"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
		{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
		{Label: "tls", Name: "/etc/ssl", Perms: "r"},
	}}, nil
}
