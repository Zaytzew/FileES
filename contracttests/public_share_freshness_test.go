package contracttests

import (
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"testing"
)

type shareSnapshotSource struct {
	result contract.PublicShareListResult
}

func (s shareSnapshotSource) List() []contract.PublicShareSummary      { return s.result.Shares }
func (s shareSnapshotSource) Snapshot() contract.PublicShareListResult { return s.result }

func TestPublicShareFreshnessCrossesRealIPC(t *testing.T) {
	server, _, sock := startEventServer(t)
	wanted := contract.PublicShareListResult{AsOf: "2026-09-20T12:00:00Z", Generation: 7, Stale: true, Shares: []contract.PublicShareSummary{{ChannelID: "retained", RepoID: "repo", ServerID: "server", ObservedAt: "2026-09-20T12:00:00Z", Stale: true}}}
	server.SetPublicShareSource(shareSnapshotSource{wanted})
	got, err := ipcclient.New(sock, "gui").PublicShareListAll(t.Context())
	if err != nil || got.AsOf != wanted.AsOf || got.Generation != 7 || !got.Stale || len(got.Shares) != 1 || !got.Shares[0].Stale || got.Shares[0].ObservedAt != wanted.AsOf {
		t.Fatalf("freshness lost: %+v %v", got, err)
	}
}
