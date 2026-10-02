//go:build linux

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The updater must leave an installation as install-ssh.sh made it. These
// are the points where the Linux backend had drifted from it (and from the
// OpenBSD backend) while nothing on Linux exercised an update.

func TestLinuxSSHDIncludeIsAppendedOutsideAnyMatchBlock(t *testing.T) {
	config := filepath.Join(t.TempDir(), "sshd_config")
	original := "Port 22\nMatch User backup\n\tForceCommand /usr/bin/rsync\n"
	if err := os.WriteFile(config, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	include := "Include /etc/ssh/sshd_config.d/*.conf"
	backup, err := ensureSSHDInclude(config, include)
	if err != nil || backup == "" {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
	raw, _ := os.ReadFile(config)
	if !strings.HasSuffix(string(raw), "\nMatch all\n"+include+"\n") || !strings.HasPrefix(string(raw), original) {
		t.Fatalf("Include would be conditional on the last Match block:\n%s", raw)
	}
	if saved, _ := os.ReadFile(backup); string(saved) != original {
		t.Fatal("backup is not the untouched original")
	}
	if again, err := ensureSSHDInclude(config, include); err != nil || again != "" {
		t.Fatalf("second run changed the file: backup=%q err=%v", again, err)
	}
}

func TestLinuxSSHDUnitNameAndState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		present    map[string]bool
		running    map[string]bool
		wantUnit   string
		wantActive bool
	}{
		{"debian", map[string]bool{"ssh": true}, map[string]bool{"ssh": true}, "ssh", true},
		{"fedora", map[string]bool{"sshd": true}, map[string]bool{"sshd": true}, "sshd", true},
		{"installed but stopped", map[string]bool{"sshd": true}, nil, "sshd", false},
		{"none", nil, nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit, active := linuxSSHDUnit(func(args ...string) error {
				switch args[0] {
				case "cat":
					if tc.present[strings.TrimSuffix(args[1], ".service")] {
						return nil
					}
				case "is-active":
					if tc.running[args[len(args)-1]] {
						return nil
					}
				}
				return errors.New("no")
			})
			if unit != tc.wantUnit || active != tc.wantActive {
				t.Fatalf("unit=%q active=%v", unit, active)
			}
		})
	}
}

func TestLinuxStateDirsMatchTheInstaller(t *testing.T) {
	dirs := linuxStateDirs(900, 901)
	byPath := map[string]linuxStateDir{}
	for _, d := range dirs {
		byPath[d.path] = d
	}
	if etc := byPath["/etc/filees"]; etc.mode != 0o750 || etc.uid != 900 || etc.gid != 901 {
		t.Fatalf("/etc/filees must stay traversable by the public group: %+v", etc)
	}
	installer, err := os.ReadFile("../../../packaging/server/linux/install-ssh.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if d.path == "/etc/filees" {
			continue
		}
		if d.mode != 0o700 || d.uid != 900 || d.gid != 0 {
			t.Errorf("%s: %+v, installer creates it 0700 state:root", d.path, d)
		}
		if !strings.Contains(string(installer), d.path) {
			t.Errorf("%s is not created by install-ssh.sh", d.path)
		}
	}
	for _, absent := range []string{"/var/run/filees", "/var/www/run/filees"} {
		if _, ok := byPath[absent]; ok {
			t.Errorf("%s belongs to public shares, which Linux installs do not configure", absent)
		}
	}
}

func TestLinuxAccountsGetAShellOnlyWhenSSHDNeedsOne(t *testing.T) {
	for name, shell := range map[string]string{
		"_filees-state": "/usr/sbin/nologin", "_filees-links": "/usr/sbin/nologin",
		"_filees-tunnel": "/bin/sh", "_filees-client": "/bin/sh", "_filees-future-entry": "/bin/sh",
	} {
		args := linuxUseraddArgs(name)
		want := []string{"--system", "--user-group", "--no-create-home", "--home-dir", "/var/empty", "--shell", shell, "--comment", "FileES " + name, name}
		if !reflect.DeepEqual(args, want) {
			t.Errorf("%s: %v", name, args)
		}
	}
}
