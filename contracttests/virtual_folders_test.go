package contracttests

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"slices"
	"testing"
)

type virtualFolderFixture struct {
	enabled bool
	refuse  bool
}

func (f *virtualFolderFixture) VirtualFolders(_ context.Context, enabled *bool) (contract.VirtualFoldersStatus, error) {
	if enabled != nil {
		if f.refuse {
			return contract.VirtualFoldersStatus{}, errors.New("attached")
		}
		f.enabled = *enabled
	}
	return contract.VirtualFoldersStatus{Enabled: f.enabled}, nil
}

func TestVirtualFoldersContractOptionalAndDurableOwner(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	fixture := &virtualFolderFixture{}
	server.SetVirtualFoldersService(fixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "virtual-test")
	hello, err := cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapVirtualFolders) {
		t.Fatal(hello, err)
	}
	state, err := cli.VirtualFolders(ctx, nil)
	if err != nil || state.Enabled {
		t.Fatal(state, err)
	}
	yes, no := true, false
	state, err = cli.VirtualFolders(ctx, &yes)
	if err != nil || !state.Enabled {
		t.Fatal(state, err)
	}
	system, err := cli.SystemStatus(ctx)
	if err != nil || system.VirtualFolders == nil || !system.VirtualFolders.Enabled {
		t.Fatal(system, err)
	}
	fixture.refuse = true
	if _, err = cli.VirtualFolders(ctx, &no); err == nil || !fixture.enabled {
		t.Fatal("refusal lost setting")
	}
	server.SetVirtualFoldersService(nil)
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapVirtualFolders) {
		t.Fatal("unavailable service advertised")
	}
	if _, err = cli.VirtualFolders(ctx, &yes); err == nil {
		t.Fatal("unsupported client enabled folders")
	}
}
