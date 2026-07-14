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
	remote int64
	local  int64
	update int
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
func (*revisionClient) Resolve(context.Context, string, []string, string, string, string) (string, error) {
	return "", nil
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
