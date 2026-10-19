package contracttests

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"github.com/google/uuid"
	"slices"
	"sync/atomic"
	"testing"
)

type conversionContractService struct {
	calls atomic.Int64
	id    string
}

func (f *conversionContractService) WCConversion(_ context.Context, p contract.WCConversionPayload) (contract.WCConversionStatus, error) {
	f.calls.Add(1)
	if p.ServerID != "lab" || p.RepoID != "repo" || p.Action == "begin" && p.SourcePath != "confirmed" {
		return contract.WCConversionStatus{}, errors.New("stale binding")
	}
	return *f.WCConversionStatus(p.ServerID, p.RepoID), nil
}
func (f *conversionContractService) WCConversionStatus(server, repo string) *contract.WCConversionStatus {
	if server != "lab" || repo != "repo" {
		return nil
	}
	return &contract.WCConversionStatus{OperationID: f.id, ServerID: server, RepoID: repo, SourcePath: "confirmed", Phase: "preserving", CountersKnown: true, Done: 3, Total: 9}
}
func TestWCConversionContractCapabilityScopeAndProgress(t *testing.T) {
	socket := testSocketPath(t)
	s := ipcserver.New(socket)
	s.RegisterActivation(contract.ActivationStatus{ServerID: "lab"})
	s.RegisterRepoAccess("repo", "svn+ssh://fixture/repo", t.TempDir(), "lab", "rw")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(socket, "conversion-contract")
	hello, err := c.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapWCConversion) {
		t.Fatal("absent service advertised", hello, err)
	}
	base := contract.WCConversionPayload{ServerID: "lab", RepoID: "repo", Action: "begin", SourcePath: "confirmed"}
	if _, err := c.WCConversion(ctx, base); err == nil {
		t.Fatal("absent service admitted")
	}
	f := &conversionContractService{id: uuid.NewString()}
	s.SetWCConversionService(f)
	hello, err = c.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapWCConversion) {
		t.Fatal(hello, err)
	}
	result, err := c.WCConversion(ctx, base)
	if err != nil || result.OperationID != f.id || result.Done != 3 || result.Total != 9 {
		t.Fatal(result, err)
	}
	for _, p := range []contract.WCConversionPayload{
		{ServerID: "other", RepoID: "repo", Action: "status"},
		{ServerID: "lab", RepoID: "repo", Action: "erase"},
		{ServerID: "lab", RepoID: "repo", Action: "retry", OperationID: "bad"},
		{ServerID: "lab", RepoID: "repo", Action: "begin", SourcePath: "confirmed", OperationID: f.id},
		{ServerID: "lab", RepoID: "repo", Action: "status", SourcePath: "arbitrary"},
	} {
		before := f.calls.Load()
		if _, err := c.WCConversion(ctx, p); err == nil || f.calls.Load() != before {
			t.Fatal("invalid request reached effects", p, err)
		}
	}
	for _, p := range []contract.WCConversionPayload{
		{ServerID: "lab", RepoID: "other", Action: "status"},
		{ServerID: "lab", RepoID: "repo", Action: "begin", SourcePath: "stale"},
	} {
		if _, err := c.WCConversion(ctx, p); err == nil {
			t.Fatal("service refusal hidden", p)
		}
	}
	status, err := c.RepoStatus(ctx, "repo")
	if err != nil || status.Conversion == nil || status.Conversion.Done != 3 {
		t.Fatal(status, err)
	}
	list, err := c.RepoList(ctx)
	if err != nil || len(list.Repos) != 1 || list.Repos[0].Conversion == nil {
		t.Fatal(list, err)
	}
	s.SetWCConversionService(nil)
	hello, err = c.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapWCConversion) {
		t.Fatal("removed service advertised", hello, err)
	}
}
