package main

import (
	"strings"
	"testing"
)

func TestPublishProfileUsesConfigToolsAndAllowedCopies(t *testing.T) {
	cfg := Config{
		SVN: defaultSVN, SVNMucc: defaultSVNMucc, SSH: defaultSSH, Port: 22,
		Identity: "/var/filees/id_ed25519", KnownHosts: "/var/filees/known_hosts",
		Copies: map[string]workingCopySpec{
			"wc-01": {ID: "wc-01", Path: "/home/ksef/books", URL: "svn+ssh://example/books"},
			"wc-02": {ID: "wc-02", Path: "/home/ksef/other", URL: "svn+ssh://example/other"},
		},
		Only: []string{"wc-01"},
	}
	profile, err := publishProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sawBooks, sawOther := false, false
	sawAccountDB, sawGroupDB := false, false
	for _, path := range profile.Paths {
		if path.Name == "/home/ksef/books" && path.Perms == "rwc" {
			sawBooks = true
		}
		if path.Name == "/home/ksef/other" {
			sawOther = true
		}
		if path.Name == defaultSVN && path.Perms != "rx" {
			t.Fatalf("svn perms = %s", path.Perms)
		}
		if path.Name == "/etc/pwd.db" && path.Perms == "r" {
			sawAccountDB = true
		}
		if path.Name == "/etc/group" && path.Perms == "r" {
			sawGroupDB = true
		}
	}
	if !sawBooks || sawOther {
		t.Fatalf("books=%v other=%v", sawBooks, sawOther)
	}
	if !sawAccountDB || !sawGroupDB {
		t.Fatalf("account-db=%v group-db=%v", sawAccountDB, sawGroupDB)
	}
}

func TestPublishPromisesKeepKsefpuckBase(t *testing.T) {
	for _, promise := range strings.Fields("stdio rpath wpath cpath fattr flock") {
		if !strings.Contains(publishPromises, promise) {
			t.Fatalf("missing ksefpuck promise %s in %q", promise, publishPromises)
		}
	}
	for _, promise := range strings.Fields("unix tty") {
		if !strings.Contains(publishChildPromises, promise) {
			t.Fatalf("missing SSH child promise %s in %q", promise, publishChildPromises)
		}
	}
}
