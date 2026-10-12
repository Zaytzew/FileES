package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"slices"
	"testing"
)

type explorerShortcutFixture struct{ values map[string]bool }

func (f *explorerShortcutFixture) ExplorerShortcut(_ context.Context, server, repo string, visible *bool, wcID string) (contract.RepoExplorerShortcutStatus, error) {
	key := server + "/" + repo
	value, ok := f.values[key]
	if !ok {
		return contract.RepoExplorerShortcutStatus{}, errors.New("not a virtual WC")
	}
	if visible != nil {
		if wcID != key {
			return contract.RepoExplorerShortcutStatus{}, errors.New("WC replaced")
		}
		value = *visible
		f.values[key] = value
	}
	return contract.RepoExplorerShortcutStatus{ServerID: server, RepoID: repo, Visible: value, WCID: key}, nil
}
func TestExplorerShortcutContractRequiresScopedOptionalService(t *testing.T) {
	socket := testSocketPath(t)
	s := ipcserver.New(socket)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(socket, "shortcut-test")
	hello, err := c.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoExplorerShortcut) {
		t.Fatal(hello, err)
	}
	yes := true
	if _, err = c.RepoExplorerShortcut(ctx, "one", "repo", &yes, "one/repo"); err == nil {
		t.Fatal("unsupported mutation")
	}
	f := &explorerShortcutFixture{values: map[string]bool{"one/repo": false, "two/repo": false}}
	s.SetExplorerShortcutService(f)
	hello, err = c.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoExplorerShortcut) {
		t.Fatal(hello, err)
	}
	if result, err := c.RepoExplorerShortcut(ctx, "one", "repo", &yes, "one/repo"); err != nil || !result.Visible || result.ServerID != "one" || result.RepoID != "repo" {
		t.Fatal(result, err)
	}
	if result, err := c.RepoExplorerShortcut(ctx, "two", "repo", nil, ""); err != nil || result.Visible {
		t.Fatal("server scope lost", result, err)
	}
	for _, p := range []string{`{"visible":true}`, `{"server_id":"one","repo_id":"repo","visible":true}`} {
		result, err := c.Do(ctx, contract.Request{Protocol: contract.Protocol, RequestID: "wrong-scope", ClientID: "shortcut-test", Command: contract.CmdRepoExplorerShortcut, RepoID: "other", Payload: json.RawMessage(p)})
		if err != nil || result.Status != contract.StatusError {
			t.Fatal("unscoped accepted", result, err)
		}
	}
	if _, err = c.RepoExplorerShortcut(ctx, "one", "ordinary", &yes, "unknown"); err == nil {
		t.Fatal("unknown accepted")
	}
	no := false
	for _, wcID := range []string{"", "previous-copy"} {
		if _, err = c.RepoExplorerShortcut(ctx, "one", "repo", &no, wcID); err == nil || !f.values["one/repo"] {
			t.Fatal("stale WC confirmation accepted")
		}
	}
}
