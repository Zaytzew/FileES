package contracttests

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestWorkingCopyRecoveryIsIndependentOfPendingAndRevision(t *testing.T) {
	socket := testSocketPath(t)
	s := ipcserver.New(socket)
	rs := s.RegisterRepoAccess("repo", "svn://fixture/repo", t.TempDir(), "lab", "rw")
	rs.SetHeadRev(51)
	p := &contract.WorkingCopyRecoveryStatus{Phase: "blocked", Detail: "exact local failure"}
	rs.SetWorkingCopyRecovery(p)
	p.Detail = "caller changed"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(socket, "local-recovery-contract")
	status, err := c.RepoStatus(ctx, "repo")
	if err != nil || status.WorkingCopyRecovery == nil || status.WorkingCopyRecovery.Detail != "exact local failure" || status.Pending.Added != 0 || status.HeadRevision != 51 {
		t.Fatal(status, err)
	}
	status.WorkingCopyRecovery.Detail = "receiver changed"
	if rs.Snapshot().WorkingCopyRecovery.Detail != "exact local failure" {
		t.Fatal("snapshot aliased live status")
	}
	rs.SetWorkingCopyRecovery(nil)
	status, err = c.RepoStatus(ctx, "repo")
	if err != nil || status.WorkingCopyRecovery != nil {
		t.Fatal(status, err)
	}
}
