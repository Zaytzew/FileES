package contracttests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

type headAvailabilityFixture struct {
	contractBatchProbe
	mu     sync.Mutex
	state  contract.RepoHeadAvailability
	fail   bool
	probes int
}

func (f *headAvailabilityFixture) HeadList(context.Context, string, string, string) ([]contract.RepoHeadEntry, error) {
	return []contract.RepoHeadEntry{{Name: "folder", Kind: "dir"}}, nil
}
func (f *headAvailabilityFixture) HeadCat(context.Context, string, string, string) (string, error) {
	return "", errors.New("unexpected content read")
}
func (f *headAvailabilityFixture) HeadAvailability(_ context.Context, server, repo, path string, entries []contract.RepoHeadEntry) (map[string]contract.RepoHeadAvailability, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if server != "server" || repo != "repo" || path != "" || len(entries) != 1 {
		return nil, true, errors.New("incorrect scope")
	}
	if f.fail {
		return nil, true, errors.New("metadata unavailable")
	}
	return map[string]contract.RepoHeadAvailability{"folder": f.state}, true, nil
}

func TestHeadAvailabilityContractSeparatesSkeletonFromContent(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	server.RegisterRepoAccess("repo", "svn://example/repo", root, "server", "rw")
	fixture := &headAvailabilityFixture{}
	server.SetHeadService(fixture)
	server.SetAnchorBatch(fixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "availability-test")
	p := contract.RepoHeadListPayload{ServerID: "server", RepoID: "repo"}
	for _, state := range []contract.RepoHeadAvailability{contract.HeadRemote, contract.HeadHybrid, contract.HeadLocal} {
		fixture.mu.Lock()
		fixture.state = state
		fixture.mu.Unlock()
		result, err := cli.HeadList(ctx, p)
		if err != nil || len(result.Entries) != 1 || !result.Entries[0].Local || result.Entries[0].Availability != state {
			t.Fatal(result, err)
		}
	}
	fixture.mu.Lock()
	fixture.fail = true
	fixture.mu.Unlock()
	result, err := cli.HeadList(ctx, p)
	if err != nil || !result.Entries[0].Local || result.Entries[0].Availability != "" {
		t.Fatal("metadata failure fabricated local content", result, err)
	}
	p.ServerID = "other"
	if _, err := cli.HeadList(ctx, p); err == nil {
		t.Fatal("unactivated scope admitted")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.probes != 4 || fixture.plans != 0 || fixture.writes != 0 {
		t.Fatal("availability performed work or bypassed scope", fixture.probes, fixture.plans, fixture.writes)
	}
}
