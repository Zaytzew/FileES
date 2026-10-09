//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

type testTreeMover struct {
	sends, inspections int
	move               func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error)
	inspect            func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error)
}

func (s *testTreeMover) RemoteTreeMovesEnabled() bool { return true }
func (s *testTreeMover) RemoteTreeMove(_ context.Context, m client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
	s.sends++
	return s.move(m)
}
func (s *testTreeMover) InspectRemoteTreeMove(_ context.Context, m client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
	s.inspections++
	return s.inspect(m)
}

func testTreeMoveIntent(t *testing.T) (localrepo.Record, client.RemoteTreeMove) {
	t.Helper()
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	r := localrepo.Record{ServerID: "fixture", RepoID: uuid.NewString(), OperationID: uuid.NewString(), LocalPath: wc, RepoURL: "file:///fixture"}
	m := client.RemoteTreeMove{RootURL: r.RepoURL, UUID: uuid.NewString(), BaseRevision: 19, OperationID: uuid.NewString(), Source: "Dokumenty źródłowe", Destination: "01_PROJEKT/Dokumenty źródłowe", Message: "Przeniesienie"}
	return r, m
}

// Journal-only fixture. Real scanner/ACK integration is tested separately;
// these observations are not an independent filesystem or SVN proof.
func testTreeMoveSnapshot(r localrepo.Record, m client.RemoteTreeMove) *watcher.TreeMoveSnapshot {
	return &watcher.TreeMoveSnapshot{Schema: "filees.scanner-tree-move/v1", WC: r.LocalPath,
		Source: m.Source, Destination: m.Destination, Session: uuid.NewString(), Sequence: 1,
		Entries: []watcher.PublicationEntry{{Path: m.Source, Present: true, IsDir: true}}}
}

func TestAnchorTreeMoveLostReplyIsInspectedNeverResent(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	lost := errors.New("lost reply after effect")
	remote := &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}
	svn := &testTreeMover{move: func(got client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		guards, root, err := guardAnchorMoveState(r)
		if err != nil {
			t.Fatal(err)
		}
		defer closeAnchorReleaseGuards(guards)
		in, sent, receipt, err := readAnchorMoveIntent(t.Context(), r, root)
		if err != nil || in.Move != m || !sent || receipt != nil || !commit.HasPendingRemoteTree(r.LocalPath) {
			t.Fatal("request was not durable before effect", in, sent, receipt, err)
		}
		return nil, lost
	}, inspect: func(got client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		if got != m {
			t.Fatal("binding changed")
		}
		return remote, nil
	}}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		receipt, err := inspectAnchorTreeMove(t.Context(), r, svn)
		if err != nil || receipt == nil || *receipt != *remote {
			t.Fatal(receipt, err)
		}
	}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn); err == nil {
		t.Fatal("pending operation replayed")
	}
	if svn.sends != 1 || svn.inspections != 2 || !commit.HasPendingRemoteTree(r.LocalPath) {
		t.Fatal(svn.sends, svn.inspections)
	}
}

func TestAnchorTreeMoveUnknownOutcomePreservesFenceAndRejectsForeignAttachment(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, errors.New("uncertain") }, inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, nil }}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn); err == nil {
		t.Fatal("lost response accepted")
	}
	if _, err := inspectAnchorTreeMove(t.Context(), r, svn); err == nil || !commit.HasPendingRemoteTree(r.LocalPath) {
		t.Fatal(err)
	}
	for _, change := range []func(*localrepo.Record){func(r *localrepo.Record) { r.ServerID = "other" }, func(r *localrepo.Record) { r.RepoID = uuid.NewString() }, func(r *localrepo.Record) { r.OperationID = uuid.NewString() }, func(r *localrepo.Record) { r.RepoURL = "file:///other" }} {
		other := r
		change(&other)
		before := svn.inspections
		if _, err := inspectAnchorTreeMove(t.Context(), other, svn); err == nil || svn.inspections != before {
			t.Fatal("foreign attachment reached SVN", err)
		}
	}
	if svn.sends != 1 {
		t.Fatal("unknown receipt resent")
	}
}

func TestAnchorTreeMoveGuardsEvidenceDuringRemoteEffect(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	probe := func() {
		for _, name := range []string{commit.RemoteTreePendingName, anchorMoveRequestName, anchorMoveScannerName} {
			p := filepath.Join(anchorMoveStateDir(r), name)
			if err := os.Remove(p); err == nil {
				t.Fatal("evidence deletion was not guarded")
			}
			if f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
				f.Close()
				t.Fatal("evidence rewrite was not guarded")
			}
		}
	}
	svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		probe()
		return &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}, nil
	}, inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		probe()
		return &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}, nil
	}}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectAnchorTreeMove(t.Context(), r, svn); err != nil {
		t.Fatal(err)
	}
}

func TestAnchorTreeMoveReplacementRootAndCanceledSendRefuse(t *testing.T) {
	r, m := testTreeMoveIntent(t)
	svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, errors.New("uncertain") }, inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
		t.Fatal("replacement root reached SVN")
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sendAnchorTreeMove(ctx, r, m, testTreeMoveSnapshot(r, m), svn); err == nil || svn.sends != 0 || commit.HasPendingRemoteTree(r.LocalPath) {
		t.Fatal("canceled send changed state", err)
	}
	if _, err := sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn); err == nil {
		t.Fatal("expected lost reply")
	}
	old := filepath.Join(t.TempDir(), "old-root")
	if err := os.Rename(r.LocalPath, old); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.RemoveAll(r.LocalPath)
		if err := os.Rename(old, r.LocalPath); err != nil {
			t.Error(err)
		}
	}()
	if err := os.MkdirAll(anchorMoveStateDir(r), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{commit.RemoteTreePendingName, anchorMoveRequestName, anchorMoveScannerName} {
		b, err := os.ReadFile(filepath.Join(old, ".filees", "state", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(anchorMoveStateDir(r), name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inspectAnchorTreeMove(t.Context(), r, svn); err == nil || svn.inspections != 0 {
		t.Fatal("root file identity not enforced", err)
	}
}

func TestAnchorTreeMovePreparedIntentAndMismatchedReceiptRefuse(t *testing.T) {
	for _, mode := range []string{"prepared", "foreign-request", "forged-local", "foreign-remote", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			r, m := testTreeMoveIntent(t)
			svn := &testTreeMover{move: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) { return nil, errors.New("uncertain") }, inspect: func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
				return &client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 20}, nil
			}}
			_, _ = sendAnchorTreeMove(t.Context(), r, m, testTreeMoveSnapshot(r, m), svn)
			dir := anchorMoveStateDir(r)
			switch mode {
			case "prepared":
				if err := os.Remove(filepath.Join(dir, anchorMoveRequestName)); err != nil {
					t.Fatal(err)
				}
			case "foreign-request":
				if err := os.WriteFile(filepath.Join(dir, anchorMoveRequestName), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "forged-local":
				guards, root, err := guardAnchorMoveState(r)
				if err != nil {
					t.Fatal(err)
				}
				in, _, _, err := readAnchorMoveIntent(t.Context(), r, root)
				closeAnchorReleaseGuards(guards)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeAnchorReleaseJSONFile(dir, anchorMoveReceiptName, anchorMoveReceipt{in, client.RemoteTreeReceipt{RemoteTreeMove: m, Revision: 21}}); err != nil {
					t.Fatal(err)
				}
			case "foreign-remote":
				svn.inspect = func(client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
					other := m
					other.Source = "other"
					return &client.RemoteTreeReceipt{RemoteTreeMove: other, Revision: 20}, nil
				}
			case "trailing":
				f, err := os.OpenFile(filepath.Join(dir, commit.RemoteTreePendingName), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString(" {}")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inspectAnchorTreeMove(t.Context(), r, svn); err == nil || !commit.HasPendingRemoteTree(r.LocalPath) {
				t.Fatal("bad evidence cleared or accepted", err)
			}
			if svn.sends != 1 {
				t.Fatal("recovery mutated SVN")
			}
		})
	}
}
