package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMinimal(t *testing.T) {
	cfg, err := Load(writeConfig(t, "[repo]\nurl = svn://example.org/FILESS-BIN/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RepoURL != "svn://example.org/FILESS-BIN" {
		t.Fatalf("trailing slash not trimmed: %q", cfg.RepoURL)
	}
	if cfg.Channel != "stable" {
		t.Fatalf("default channel: %q", cfg.Channel)
	}
	if cfg.DefaultAction != "check" || cfg.ConfigDrift != "block" || cfg.OrphanFiles != "keep" {
		t.Fatalf("policy defaults: %q %q %q", cfg.DefaultAction, cfg.ConfigDrift, cfg.OrphanFiles)
	}
	if !cfg.RequireHash || !cfg.VerifySignature {
		t.Fatalf("hash/signature defaults: %v %v", cfg.RequireHash, cfg.VerifySignature)
	}
	if cfg.StateDir != "/var/filees/install-state" || cfg.SbinDir != "/usr/local/sbin" {
		t.Fatalf("path defaults: %q %q", cfg.StateDir, cfg.SbinDir)
	}
}

func TestLoadFullOverride(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Join([]string{
		"[repo]",
		"url = svn://h/BIN",
		"channel = testing",
		"platform = openbsd-amd64",
		"[install]",
		"sbin_dir = /opt/sbin",
		"sshd_conf_dir = /etc/ssh/frag.d",
		"[policy]",
		"default_action = apply",
		"config_drift = warn",
		"orphan_files = remove",
		"interactive = no",
		"require_hash = on",
		"verify_signature = yes",
		"talkative = 1",
		"[security]",
		"signify = /usr/bin/signify",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channel != "testing" || cfg.Platform != "openbsd-amd64" {
		t.Fatalf("repo section: %q %q", cfg.Channel, cfg.Platform)
	}
	if cfg.SbinDir != "/opt/sbin" || cfg.SSHDConfDir != "/etc/ssh/frag.d" {
		t.Fatalf("install section: %q %q", cfg.SbinDir, cfg.SSHDConfDir)
	}
	if cfg.DefaultAction != "apply" || cfg.ConfigDrift != "warn" || cfg.OrphanFiles != "remove" {
		t.Fatalf("policy enums: %q %q %q", cfg.DefaultAction, cfg.ConfigDrift, cfg.OrphanFiles)
	}
	if cfg.Interactive || !cfg.RequireHash || !cfg.VerifySignature || !cfg.Talkative {
		t.Fatalf("policy bools: %v %v %v %v", cfg.Interactive, cfg.RequireHash, cfg.VerifySignature, cfg.Talkative)
	}
	if cfg.SignifyProgram != "/usr/bin/signify" {
		t.Fatalf("signify: %q", cfg.SignifyProgram)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"missing repo.url":   "[policy]\ndefault_action = check\n",
		"unknown key":        "[repo]\nurl = svn://h/B\nbogus = 1\n",
		"bad enum":           "[repo]\nurl = svn://h/B\n[policy]\ndefault_action = destroy\n",
		"bad bool":           "[repo]\nurl = svn://h/B\n[policy]\ninteractive = maybe\n",
		"bad line":           "[repo]\nurl = svn://h/B\nno separator here\n",
		"hash disabled":      "[repo]\nurl = svn://h/B\n[policy]\nrequire_hash = no\n",
		"signature disabled": "[repo]\nurl = svn://h/B\n[policy]\nverify_signature = no\n",
	}
	for name, content := range cases {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestLoadIgnoresCommentsAndBlank(t *testing.T) {
	cfg, err := Load(writeConfig(t, "# comment\n\n[repo]\n# another\nurl = svn://h/B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RepoURL != "svn://h/B" {
		t.Fatalf("url: %q", cfg.RepoURL)
	}
}

func TestFindPrefersCLI(t *testing.T) {
	got, err := Find("/some/explicit.conf")
	if err != nil || got != "/some/explicit.conf" {
		t.Fatalf("Find(cli): %q %v", got, err)
	}
}
