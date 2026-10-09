package contracttests

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"slices"
	"strings"
	"testing"
)

type moveContractProbe struct {
	plans, writes, recoveries int
	err                       error
}

func (m *moveContractProbe) MovePlan(_ context.Context, p contract.RepoAnchorMoveSelection) (contract.RepoAnchorMovePlan, error) {
	m.plans++
	return contract.RepoAnchorMovePlan{Source: p.Source, Destination: p.Destination, Fingerprint: strings.Repeat("ab", 32)}, m.err
}
func (m *moveContractProbe) MoveExecute(context.Context, contract.RepoAnchorMovePayload) error {
	m.writes++
	return m.err
}
func (m *moveContractProbe) MoveRecover(context.Context, string, string) error {
	m.recoveries++
	return m.err
}
func TestAnchorMoveContract(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", t.TempDir(), "server", "rw")
	repo.SetSparse(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "move-test")
	p := contract.RepoAnchorMoveSelection{ServerID: "server", RepoID: "repo", Source: "źródło", Destination: "01_PROJEKT/cel"}
	hello, err := cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorMove) {
		t.Fatal("unavailable advertised", err)
	}
	if _, err := cli.AnchorMovePlan(ctx, p); err == nil {
		t.Fatal("missing provider accepted")
	}
	probe := &moveContractProbe{}
	server.SetAnchorMove(probe)
	hello, err = cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoAnchorMove) {
		t.Fatal("provider not advertised", err)
	}
	plan, err := cli.AnchorMovePlan(ctx, p)
	if err != nil || probe.writes != 0 || plan.Source != p.Source {
		t.Fatal(plan, err)
	}
	if _, err := cli.AnchorMove(ctx, contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: p}); err == nil || probe.writes != 0 {
		t.Fatal("unconfirmed move")
	}
	if _, err := cli.AnchorMove(ctx, contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: p, Fingerprint: plan.Fingerprint}); err != nil || probe.writes != 1 {
		t.Fatal(err)
	}
	for _, bad := range []string{"", ".", "../a", "a\\b", "a\tb", ".svn/db", "dir/.FILEES/state", " x", "x/", "źródło/child", "ŹRÓDŁO"} {
		badP := p
		badP.Destination = bad
		if _, err := cli.AnchorMovePlan(ctx, badP); err == nil {
			t.Fatal("invalid destination admitted", bad)
		}
	}
	if probe.plans != 1 {
		t.Fatal("bad paths reached provider", probe.plans)
	}
	if _, err := cli.AnchorMoveRecover(ctx, p); err == nil || probe.recoveries != 0 {
		t.Fatal("recovery accepted replacement scope")
	}
	if _, err := cli.AnchorMoveRecover(ctx, contract.RepoAnchorMoveSelection{ServerID: p.ServerID, RepoID: p.RepoID}); err != nil || probe.recoveries != 1 {
		t.Fatal(err)
	}
	server.RegisterRepoAccess("repo", "svn://example/repo", repo.Summary().LocalPath, "server", "r").SetSparse(true)
	if _, err := cli.AnchorMovePlan(ctx, p); err == nil || probe.plans != 1 {
		t.Fatal("read-only reached writer")
	}
	server.RegisterRepoAccess("repo", "svn://example/repo", repo.Summary().LocalPath, "server", "rw").SetSparse(true)
	probe.err = ipcserver.ErrAnchorSelectionChanged
	_, err = cli.AnchorMove(ctx, contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: p, Fingerprint: plan.Fingerprint})
	var failure *ipcclient.ResponseError
	if !errors.As(err, &failure) || failure.Body.MessageKey != "head.folder_changed" {
		t.Fatal(err)
	}
	server.SetAnchorMove(nil)
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorMove) {
		t.Fatal(err)
	}
}
