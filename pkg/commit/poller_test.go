package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/talk"
)

type revisionClient struct {
	remote   int64
	local    int64
	update   int
	resolved []string
	accept   string
}

func (c *revisionClient) Revision(_ context.Context, target, _, _ string) (int64, error) {
	if filepath.IsAbs(target) {
		return c.local, nil
	}
	return c.remote, nil
}
func (c *revisionClient) Update(context.Context, string, string, string) (string, error) {
	c.update++
	return "", nil
}
func (*revisionClient) GetInfo(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Checkout(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Cleanup(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Status(context.Context, string, []string, string, string) ([]client.StatusEntry, error) {
	return nil, nil
}
func (*revisionClient) Add(context.Context, string, []string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Delete(context.Context, string, []string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Commit(context.Context, string, []string, string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Lock(context.Context, string, []string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) Unlock(context.Context, string, []string, string, string) (string, error) {
	return "", nil
}
func (*revisionClient) PropGet(context.Context, string, string, []string, string, string) (string, error) {
	return "", nil
}

func (c *revisionClient) Resolve(_ context.Context, _ string, paths []string, accept, _, _ string) (string, error) {
	c.resolved = append(c.resolved, paths...)
	c.accept = accept
	return "", nil
}

func TestReconcileUpdateConflictsPreservesLocalCopy(t *testing.T) {
	wc := t.TempDir()
	rel := "docs/report.txt"
	abs := filepath.Join(wc, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("local version"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &revisionClient{}
	service := &Service{Cli: cli, Logger: talk.With("startup-reconcile-test")}

	service.ReconcileUpdateConflicts(context.Background(), wc, "", "", "   C docs/report.txt\n")

	if len(cli.resolved) != 1 || cli.resolved[0] != rel || cli.accept != "theirs-full" {
		t.Fatalf("Resolve = paths %v accept %q", cli.resolved, cli.accept)
	}
	matches, err := filepath.Glob(filepath.Join(wc, kolizjeDir, "*_lokalne", "docs", "report.txt"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("saved copies = %v, err = %v", matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil || string(b) != "local version" {
		t.Fatalf("saved local copy = %q, err = %v", b, err)
	}
	if _, err := os.Stat(matches[0] + ".meta"); err != nil {
		t.Fatalf("conflict metadata: %v", err)
	}
}

func TestPollOncePersistsRevisionWhenWorkingCopyAlreadyAtHead(t *testing.T) {
	wc := t.TempDir()
	path := filepath.Join(wc, ".filees", "state", "head.rev")
	cli := &revisionClient{remote: 61, local: 61}
	service := &Service{Cli: cli, RepoURL: "svn://example/repo", Logger: talk.With("poll-test")}

	service.pollOnce(context.Background(), wc, "", "", path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "61\n" {
		t.Fatalf("head.rev = %q, want 61", data)
	}
	if cli.update != 0 {
		t.Fatalf("Update calls = %d, want 0", cli.update)
	}
}
