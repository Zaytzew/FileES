package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

type lifecycleProbe struct {
	client.Client
	url       string
	mutations int
}

func (p *lifecycleProbe) GetInfo(context.Context, string) (string, error) {
	return "URL: " + p.url + "\n", nil
}
func (p *lifecycleProbe) Checkout(_ context.Context, url, root string) (string, error) {
	p.mutations++
	p.url = url
	return "", os.MkdirAll(filepath.Join(root, ".svn"), 0700)
}
func (p *lifecycleProbe) Update(context.Context, string) (string, error) {
	p.mutations++
	return "", nil
}
func (p *lifecycleProbe) Cleanup(context.Context, string) (string, error) {
	p.mutations++
	return "", nil
}

func TestLifecycleAuthorityPrecedesResumeMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	probe := &lifecycleProbe{url: "file:///foreign"}
	expected := expectedWorkingCopyIdentity("server", "repo", "file:///expected")
	svn := lifecycleSVN{Client: probe, expected: func(string, string) (workingCopyIdentity, error) { return expected, nil }}
	if _, err := svn.Checkout(ctx, expected.RepoURL, root); err == nil {
		t.Fatal("foreign WC accepted")
	}
	if probe.mutations != 0 {
		t.Fatal("mutation before URL proof")
	}
	if _, err := os.Stat(filepath.Join(root, ".filees")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("foreign WC stamped", err)
	}
	probe.url = expected.RepoURL
	if _, err := svn.Checkout(ctx, expected.RepoURL, root); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkingCopyIdentity(root, expected); err != nil {
		t.Fatal(err)
	}
	probe.mutations = 0
	expected.RepoID = "another"
	if _, err := svn.Checkout(ctx, expected.RepoURL, root); err == nil || probe.mutations != 0 {
		t.Fatal("foreign marker accepted", err)
	}
}

func TestServiceProjectionPreparesIdentityForEveryMutation(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "service-wc")
	probe := &lifecycleProbe{}
	updater := serviceProjectionUpdater{client: probe, url: "file:///service/", prepare: serviceWCPreparation(probe, "server", "client", "file:///service/")}
	if _, err := updater.Update(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkingCopyIdentity(root, expectedWorkingCopyIdentity("server", "service/client", updater.url)); err != nil {
		t.Fatal(err)
	}
	probe.url = "file:///foreign"
	probe.mutations = 0
	if _, err := updater.Update(ctx, root); err == nil {
		t.Fatal("foreign update")
	}
	if _, err := updater.Cleanup(ctx, root); err == nil {
		t.Fatal("foreign cleanup")
	}
	if probe.mutations != 0 {
		t.Fatal("foreign WC mutated")
	}
}

func TestIdentityRejectsLinkedMetadata(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".filees")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ensureWorkingCopyIdentity(root, expectedWorkingCopyIdentity("s", "r", "file:///r")); err == nil {
		t.Fatal("linked metadata accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote outside WC", entries, err)
	}
}
