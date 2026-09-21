package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyRestrictsRealmCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.conf")
	body := `
[tools]
svn=/usr/local/bin/svn
ssh=/usr/bin/ssh
[realm]
identity=/var/filees/id_ed25519
known_hosts=/var/filees/known_hosts
port=2223
only=!wc-01
[wc "wc-01"]
path=/home/ksef/books
url=svn+ssh://_filees-client@example/books
[wc "wc-02"]
path=/home/ksef/other
url=svn+ssh://_filees-client@example/other
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SVN != defaultSVN || cfg.SSH != defaultSSH {
		t.Fatalf("tools = %s %s", cfg.SVN, cfg.SSH)
	}
	if _, err := cfg.selectCopy("wc-02"); err == nil {
		t.Fatal("wc-02 was allowed")
	}
	got, err := cfg.selectCopy("wc-01")
	if err != nil || got.Path != "/home/ksef/books" {
		t.Fatalf("wc-01 = %+v %v", got, err)
	}
	if _, err := parseOnly("wc-01"); err == nil || !strings.Contains(err.Error(), "!wc-01") {
		t.Fatalf("bare id err = %v", err)
	}
}

func TestDefaultsWhenToolsOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.conf")
	body := `
[realm]
identity=/var/filees/id_ed25519
known_hosts=/var/filees/known_hosts
[wc "wc-01"]
path=/home/ksef/books
url=svn+ssh://_filees-client@example/books
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SVN != "/usr/local/bin/svn" || cfg.SSH != "/usr/bin/ssh" {
		t.Fatalf("defaults = %s %s", cfg.SVN, cfg.SSH)
	}
	if len(cfg.allowed()) != 1 {
		t.Fatalf("allowed = %#v", cfg.allowed())
	}
}
