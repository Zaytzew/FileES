package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"maps"
	"slices"
	"testing"
)

type virtualFolderFixture struct {
	enabled map[string]bool
	refuse  bool
}

func (f *virtualFolderFixture) VirtualFolders(_ context.Context, serverID string, enabled *bool) (contract.VirtualFoldersStatus, error) {
	if enabled != nil {
		if f.refuse {
			return contract.VirtualFoldersStatus{}, errors.New("attached")
		}
		f.enabled[serverID] = *enabled
	}
	if serverID == "" {
		return contract.VirtualFoldersStatus{Servers: maps.Clone(f.enabled)}, nil
	}
	return contract.VirtualFoldersStatus{ServerID: serverID, Enabled: f.enabled[serverID]}, nil
}

func TestVirtualFoldersContractOptionalAndDurableOwner(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	fixture := &virtualFolderFixture{enabled: map[string]bool{}}
	server.SetVirtualFoldersService(fixture)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "virtual-test")
	hello, err := cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapVirtualFolders) || slices.Contains(hello.Capabilities, "system.virtual_folders") {
		t.Fatal(hello, err)
	}
	state, err := cli.VirtualFolders(ctx, "lab", nil)
	if err != nil || state.Enabled {
		t.Fatal(state, err)
	}
	yes, no := true, false
	if _, err = cli.VirtualFolders(ctx, "", &yes); err == nil || fixture.enabled["lab"] {
		t.Fatal("unscoped decision accepted")
	}
	state, err = cli.VirtualFolders(ctx, "lab", &yes)
	if err != nil || !state.Enabled || state.ServerID != "lab" {
		t.Fatal(state, err)
	}
	state, err = cli.VirtualFolders(ctx, "other", nil)
	if err != nil || state.Enabled || state.ServerID != "other" {
		t.Fatal("IPC scope lost", state, err)
	}
	legacy, err := cli.Do(ctx, contract.Request{Protocol: contract.Protocol, RequestID: "legacy-test", ClientID: "virtual-test", Command: "system.virtual_folders", Payload: json.RawMessage(`{"enabled":true}`)})
	if err != nil || legacy.Status != contract.StatusError {
		t.Fatal("legacy global mutation accepted")
	}
	system, err := cli.SystemStatus(ctx)
	if err != nil || system.VirtualFolders == nil || !system.VirtualFolders.Servers["lab"] || system.VirtualFolders.Enabled {
		t.Fatal(system, err)
	}
	fixture.refuse = true
	if _, err = cli.VirtualFolders(ctx, "lab", &no); err == nil || !fixture.enabled["lab"] {
		t.Fatal("refusal lost setting")
	}
	server.SetVirtualFoldersService(nil)
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapVirtualFolders) {
		t.Fatal("unavailable service advertised")
	}
	if _, err = cli.VirtualFolders(ctx, "lab", &yes); err == nil {
		t.Fatal("unsupported client enabled folders")
	}
}
