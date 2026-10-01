//go:build windows

package msistartup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitialChannelCancelInvalidAndPreserve(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	for _, choose := range []func(string) error{
		func(string) error { return ErrCancelled },
		func(string) error { return nil },
		func(p string) error {
			return os.WriteFile(p, []byte(`{"update":{"enabled":true,"channel":"stable"}}`), 0600)
		},
	} {
		if err := EnsureConfig(path, root, choose); err == nil {
			t.Fatal("accepted unfinished setup")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("created config")
		}
		matches, _ := filepath.Glob(filepath.Join(root, ".filees-setup-*"))
		if len(matches) != 0 {
			t.Fatal("leaked provisional")
		}
	}
	choose := func(p string) error {
		return os.WriteFile(p, []byte(`{"update":{"enabled":true,"channel":"beta"}}`), 0600)
	}
	if err := EnsureConfig(path, root, choose); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := EnsureConfig(path, root, func(string) error { t.Fatal("asked again"); return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("changed existing config")
	}
}

func TestInitialConfigConcurrentCreationWins(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	err := EnsureConfig(path, root, func(p string) error {
		if err := os.WriteFile(path, []byte("owner-config"), 0600); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(`{"update":{"enabled":true,"channel":"alpha"}}`), 0600)
	})
	if err == nil {
		t.Fatal("overwrote concurrent config")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "owner-config" {
		t.Fatal("lost owner config")
	}
}

func TestStorePredecessorDecisions(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		installed, show, removed, cancel, want, fail bool
	}{
		{"absent", false, true, false, false, true, false},
		{"autostart yields", true, false, false, false, false, false},
		{"cancel", true, true, false, true, false, false},
		{"false success", true, true, false, false, false, true},
		{"removed", true, true, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			present := tc.installed
			asks := 0
			ok, err := ResolveStore(tc.show, func() (bool, error) { return present, nil }, func() error {
				asks++
				if tc.cancel {
					return ErrCancelled
				}
				if tc.removed {
					present = false
				}
				return nil
			})
			if ok != tc.want || (err != nil) != tc.fail {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if (!tc.show || !tc.installed) && asks != 0 {
				t.Fatal("asked unexpectedly")
			}
		})
	}
	_, err := ResolveStore(true, func() (bool, error) { return false, errors.New("API failure") }, func() error { t.Fatal("asked after API failure"); return nil })
	if err == nil {
		t.Fatal("ignored package API failure")
	}
}
