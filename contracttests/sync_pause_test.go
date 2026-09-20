package contracttests

import (
	"context"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/runtime"
	"slices"
	"testing"
)

func TestIPCDraftAndManualPauseRemainIndependent(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	rs := s.RegisterRepo("a", "svn://example/a", t.TempDir())
	rs.SetPublishFunc(func(ctx context.Context, _ string) (int64, error) {
		leave, err := s.SyncPause().Enter(ctx, "a", true)
		if err != nil {
			return 0, err
		}
		defer leave()
		return 9, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(sock, "contract")
	h, err := c.Hello(ctx)
	if err != nil || !slices.Contains(h.Capabilities, contract.CapShoutDraft) || !slices.Contains(h.Capabilities, contract.CapSyncPause) {
		t.Fatal(h, err)
	}
	draft, finish, err := c.BeginShoutDraft(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	status, err := c.SystemStatus(ctx)
	if err != nil || !status.SyncPause.Draft {
		t.Fatal(status, err)
	}
	if _, err = s.SyncPause().Enter(ctx, "other", true); !errors.Is(err, runtime.ErrSyncPaused) {
		t.Fatal(err)
	}
	if _, err = c.RepoPublish(ctx, "a", "unguarded"); err == nil {
		t.Fatal("publish bypassed editor")
	}
	if err = c.SyncPause(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RepoPublish(draft, "a", "manual pause wins"); err == nil {
		t.Fatal("manual pause ignored")
	}
	if err = c.SyncPause(ctx, false); err != nil {
		t.Fatal(err)
	}
	result, err := c.RepoPublish(draft, "a", "ready")
	if err != nil || result.Revision != 9 {
		t.Fatal(result, err)
	}
	if err = c.SyncPause(ctx, true); err != nil {
		t.Fatal(err)
	}
	finish()
	status, err = c.SystemStatus(ctx)
	if err != nil || status.SyncPause.Draft || !status.SyncPause.Manual {
		t.Fatal(status, err)
	}
	if err = c.SyncPause(ctx, false); err != nil {
		t.Fatal(err)
	}
	leave, err := s.SyncPause().Enter(ctx, "other", true)
	if err != nil {
		t.Fatal(err)
	}
	leave()
}
func TestIPCDraftReleasedOnGUICancellation(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	s.RegisterRepo("a", "svn://example/a", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(sock, "contract")
	gui, closeGUI := context.WithCancel(ctx)
	draft, end, err := c.BeginShoutDraft(gui, "a")
	if err != nil {
		t.Fatal(err)
	}
	closeGUI()
	<-draft.Done()
	end()
	status, err := c.SystemStatus(ctx)
	if err != nil || status.SyncPause.Draft {
		t.Fatal(status, err)
	}
}
