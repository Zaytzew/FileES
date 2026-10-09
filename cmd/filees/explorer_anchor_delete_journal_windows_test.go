//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/watcher"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type testFileDeleter struct {
	sends, inspections int
	send               func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error)
	inspect            func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error)
}

func (s *testFileDeleter) RemoteFileDeletesEnabled() bool { return true }
func (s *testFileDeleter) RemoteFileDelete(_ context.Context, in client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
	s.sends++
	return s.send(in)
}
func (s *testFileDeleter) InspectRemoteFileDelete(_ context.Context, in client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
	s.inspections++
	return s.inspect(in)
}

// Journal/attribute guard unit fixture, NOT proof of Cloud Filter or current
// authorization. The physical file is ordinary; metadata is a controlled fake.
func testDeleteJournal(t *testing.T) (*anchorDeleteSession, anchorDeleteCallback, client.RemoteFileDelete, *watcher.FileDeleteSnapshot, func(context.Context) (cfapiAnswer, error)) {
	t.Helper()
	r, move := testTreeMoveIntent(t)
	in := client.RemoteFileDelete{RootURL: r.RepoURL, UUID: move.UUID, BaseRevision: 19, OperationID: uuid.NewString(), Source: "dokument źródłowy.bin", Message: "Usunięcie pliku"}
	if err := os.WriteFile(filepath.Join(r.LocalPath, in.Source), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	guards, root, err := guardAnchorMoveState(r)
	if err != nil {
		t.Fatal(err)
	}
	closeAnchorReleaseGuards(guards)
	h, info, err := pinMoveObject(r, in.Source, true)
	if err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
	s := newAnchorDeleteSession(r, 111)
	t.Cleanup(s.close)
	identity, _ := anchorRemoteIdentity(in.Source, in.BaseRevision, 4)
	cb := anchorDeleteCallback{Session: s.id, Volume: root.Volume, RootFileID: root.Index, FileID: anchorReleaseID(info).Index,
		RequestKey: 7, ProcessID: 222, Identity: identity,
		NormalizedPath: strings.TrimPrefix(filepath.Join(r.LocalPath, in.Source), filepath.VolumeName(r.LocalPath))}
	p := &watcher.FileDeleteSnapshot{Schema: "filees.scanner-file-delete/v1", WC: r.LocalPath, Source: in.Source, Session: uuid.NewString(), Sequence: 1,
		Entry: watcher.PublicationEntry{Path: in.Source, Present: true, Size: 4}}
	size := int64(4)
	inspect := func(context.Context) (cfapiAnswer, error) {
		return cfapiAnswer{OK: true, Placeholder: true, InSync: true, Identity: identity, Size: &size, USN: "100"}, nil
	}
	return s, cb, in, p, inspect
}

func prepareDeleteJournal(t *testing.T, s *anchorDeleteSession, cb anchorDeleteCallback, in client.RemoteFileDelete, p *watcher.FileDeleteSnapshot, inspect func(context.Context) (cfapiAnswer, error)) {
	t.Helper()
	if err := s.prepare(t.Context(), cb, in, p, inspect); err != nil {
		t.Fatal(err)
	}
}

func completeDeleteJournal(t *testing.T, s *anchorDeleteSession, cb anchorDeleteCallback, in client.RemoteFileDelete) {
	t.Helper()
	if err := os.Remove(filepath.Join(s.record.LocalPath, in.Source)); err != nil {
		t.Fatal(err)
	}
	cb.RequestKey = 0
	if err := s.complete(t.Context(), cb); err != nil {
		t.Fatal(err)
	}
}

func TestAnchorDeletePreparedAndLostCompletionNeverPublish(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission-only", true: "deleted-without-completion"}[remove], func(t *testing.T) {
			s, cb, in, p, inspect := testDeleteJournal(t)
			prepareDeleteJournal(t, s, cb, in, p, inspect)
			if !commit.HasPendingRemoteFileDelete(s.record.LocalPath) {
				t.Fatal("ACK without durable fence")
			}
			if remove {
				if err := os.Remove(filepath.Join(s.record.LocalPath, in.Source)); err != nil {
					t.Fatal(err)
				}
			}
			s.close()
			fresh := newAnchorDeleteSession(s.record, s.providerPID)
			defer fresh.close()
			cb.RequestKey = 0
			if err := fresh.complete(t.Context(), cb); err == nil {
				t.Fatal("new session completed an old intent")
			}
			svn := &testFileDeleter{send: func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
				t.Fatal("permission or absence authorized publish")
				return nil, nil
			}, inspect: func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
				t.Fatal("prepared reached SVN")
				return nil, nil
			}}
			if _, err := sendAnchorFileDelete(t.Context(), s.record, svn); err == nil {
				t.Fatal("uncompleted send admitted")
			}
			if _, _, err := inspectAnchorFileDelete(t.Context(), s.record, svn); err == nil {
				t.Fatal("uncompleted recovery admitted")
			}
			if !commit.HasPendingRemoteFileDelete(s.record.LocalPath) {
				t.Fatal("HOLD cleared fence")
			}
		})
	}
}

func TestAnchorDeleteExactCompletionAndLostRemoteReplyNeverReplay(t *testing.T) {
	s, cb, in, p, inspect := testDeleteJournal(t)
	prepareDeleteJournal(t, s, cb, in, p, inspect)
	cb.RequestKey++
	prepareDeleteJournal(t, s, cb, in, p, inspect) // duplicate pre, not a second intent
	completeDeleteJournal(t, s, cb, in)
	cb.RequestKey = 0
	if err := s.complete(t.Context(), cb); err != nil {
		t.Fatal("duplicate completion not idempotent", err)
	}
	s.close()
	replacement := filepath.Join(s.record.LocalPath, in.Source)
	if err := os.WriteFile(replacement, []byte("later local recreation"), 0600); err != nil {
		t.Fatal(err)
	}
	lost := errors.New("lost response after remote effect")
	receipt := &client.RemoteFileDeleteReceipt{RemoteFileDelete: in, Revision: 20}
	svn := &testFileDeleter{send: func(got client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
		if got != in {
			t.Fatal("selection changed")
		}
		for _, name := range []string{commit.RemoteFileDeletePendingName, anchorDeleteCompletedName, anchorDeleteRequestedName} {
			file := filepath.Join(anchorMoveStateDir(s.record), name)
			if err := os.Remove(file); err == nil {
				t.Fatal("evidence unpinned during effect")
			}
			if f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
				f.Close()
				t.Fatal("evidence rewrite admitted")
			}
		}
		return nil, lost
	}, inspect: func(got client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
		if got != in {
			t.Fatal("inspection changed binding")
		}
		return receipt, nil
	}}
	if _, err := sendAnchorFileDelete(t.Context(), s.record, svn); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, snapshot, err := inspectAnchorFileDelete(t.Context(), s.record, svn)
		if err != nil || got == nil || *got != *receipt || snapshot == nil || *snapshot != *p {
			t.Fatal(got, snapshot, err)
		}
	}
	if _, err := sendAnchorFileDelete(t.Context(), s.record, svn); err == nil || svn.sends != 1 || svn.inspections != 2 {
		t.Fatal("uncertain request replayed", err, svn)
	}
	raw, _ := os.ReadFile(replacement)
	if string(raw) != "later local recreation" || !commit.HasPendingRemoteFileDelete(s.record.LocalPath) {
		t.Fatal("recreation changed or remote receipt cleared local fence")
	}
}

func TestAnchorDeleteRejectsForeignCallbacksAndSerializesDuplicates(t *testing.T) {
	s, cb, in, p, inspect := testDeleteJournal(t)
	prepareDeleteJournal(t, s, cb, in, p, inspect)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			duplicate := cb
			duplicate.RequestKey = int64(i + 1)
			if err := s.prepare(t.Context(), duplicate, in, p, inspect); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for _, change := range []func(*anchorDeleteCallback){
		func(c *anchorDeleteCallback) { c.Session = uuid.NewString() }, func(c *anchorDeleteCallback) { c.Volume++ }, func(c *anchorDeleteCallback) { c.RootFileID++ },
		func(c *anchorDeleteCallback) { c.FileID++ }, func(c *anchorDeleteCallback) { c.ProcessID++ }, func(c *anchorDeleteCallback) { c.Identity = "other" },
		func(c *anchorDeleteCallback) { c.NormalizedPath += "-other" }, func(c *anchorDeleteCallback) { c.Flags = 1 },
	} {
		foreign := cb
		change(&foreign)
		if err := s.complete(t.Context(), foreign); err == nil {
			t.Fatal("foreign completion accepted", foreign)
		}
		if err := s.prepare(t.Context(), foreign, in, p, inspect); err == nil {
			t.Fatal("foreign duplicate pre accepted", foreign)
		}
	}
	if err := s.complete(t.Context(), cb); err == nil {
		t.Fatal("completion accepted with old object still present")
	}
	if _, err := os.Stat(filepath.Join(anchorMoveStateDir(s.record), anchorDeleteCompletedName)); !os.IsNotExist(err) {
		t.Fatal("false completion persisted", err)
	}
	completeDeleteJournal(t, s, cb, in)
}

func TestAnchorDeletePrepareRefusalsHaveNoFence(t *testing.T) {
	for _, mode := range []string{"own-projection", "directory", "undelete", "request-zero", "stale-identity", "different-file", "wrong-source", "future-wc", "dirty", "partial", "resident", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, cb, in, p, inspect := testDeleteJournal(t)
			ctx := t.Context()
			switch mode {
			case "own-projection":
				cb.ProcessID = s.providerPID
			case "directory":
				cb.Flags = 1
			case "undelete":
				cb.Flags = 2
			case "request-zero":
				cb.RequestKey = 0
			case "stale-identity":
				cb.Identity, _ = anchorRemoteIdentity(in.Source, 18, 4)
			case "different-file":
				cb.FileID++
			case "wrong-source":
				p.Source = "other"
			case "future-wc":
				p.WC = filepath.Join(p.WC, "other")
			case "dirty", "partial", "resident":
				old := inspect
				inspect = func(ctx context.Context) (cfapiAnswer, error) {
					a, e := old(ctx)
					if mode == "dirty" {
						a.Modified = 1
					}
					if mode == "partial" {
						a.Partial = true
					}
					if mode == "resident" {
						a.Local = true
					}
					return a, e
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := s.prepare(ctx, cb, in, p, inspect); err == nil || commit.HasPendingRemoteFileDelete(s.record.LocalPath) {
				t.Fatal("refusal changed state", mode, err)
			}
		})
	}
}

func TestAnchorDeleteEvidenceAndAttachmentRefuseBeforeInspection(t *testing.T) {
	for _, mode := range []string{"completion-corrupt", "intent-trailing", "foreign-attachment", "foreign-root-url", "parent-replaced", "unknown-receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, cb, in, p, inspect := testDeleteJournal(t)
			prepareDeleteJournal(t, s, cb, in, p, inspect)
			completeDeleteJournal(t, s, cb, in)
			s.close()
			svn := &testFileDeleter{send: func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
				return nil, errors.New("uncertain")
			}, inspect: func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) { return nil, nil }}
			if _, err := sendAnchorFileDelete(t.Context(), s.record, svn); err == nil {
				t.Fatal("expected uncertain outcome")
			}
			r := s.record
			switch mode {
			case "completion-corrupt":
				if err := os.WriteFile(filepath.Join(anchorMoveStateDir(r), anchorDeleteCompletedName), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "intent-trailing":
				f, err := os.OpenFile(filepath.Join(anchorMoveStateDir(r), commit.RemoteFileDeletePendingName), os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString(" {}")
				f.Close()
			case "foreign-attachment":
				r.OperationID = uuid.NewString()
			case "foreign-root-url":
				r.RepoURL = "file:///other"
			case "parent-replaced":
				old := filepath.Join(t.TempDir(), "old-root")
				if err := os.Rename(r.LocalPath, old); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(anchorMoveStateDir(r), 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{commit.RemoteFileDeletePendingName, anchorDeleteCompletedName, anchorDeleteRequestedName} {
					raw, err := os.ReadFile(filepath.Join(old, ".filees", "state", name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(anchorMoveStateDir(r), name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "unknown-receipt": // nil remote result is HOLD, never a retry permission.
			}
			if _, _, err := inspectAnchorFileDelete(t.Context(), r, svn); err == nil {
				t.Fatal("bad evidence accepted")
			}
			if mode != "unknown-receipt" && svn.inspections != 0 {
				t.Fatal("bad local binding reached remote inspection")
			}
			if svn.sends != 1 || !commit.HasPendingRemoteFileDelete(r.LocalPath) {
				t.Fatal("recovery replayed or cleared fence")
			}
		})
	}
}

func TestAnchorDeleteJournalFeedsRealWriterAndFreshScanner(t *testing.T) {
	s, cb, in, _, inspect := testDeleteJournal(t)
	wc := s.record.LocalPath
	manifest := filepath.Join(anchorMoveStateDir(s.record), "manifest.json")
	scanner, err := watcher.NewScanner(watcher.Options{WC: wc, StatePath: manifest, PublicationPending: func() bool { return commit.HasPendingLocalMutation(wc) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	events := scanner.Start(ctx)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := scanner.CapturePublication([]string{in.Source})
		if err != nil {
			t.Fatal(err)
		}
		if p.Sequence != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial scan unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	for range events {
	}
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	service := &commit.Service{Cli: client.New(client.Options{})}
	service.BindWorkingCopy(s.record.RepoID, wc)
	if err := service.WithRemoteFileDeleteMutation(t.Context(), wc, false, func() error {
		return scanner.WithFileDeletePreparation(t.Context(), in.Source, func(p *watcher.FileDeleteSnapshot) error {
			return s.prepare(t.Context(), cb, in, p, inspect)
		})
	}); err != nil {
		t.Fatal(err)
	}
	completeDeleteJournal(t, s, cb, in)
	s.close()
	if err := os.WriteFile(filepath.Join(wc, in.Source), []byte("new work after original deletion"), 0600); err != nil {
		t.Fatal(err)
	}
	lost := errors.New("remote response lost")
	svn := &testFileDeleter{send: func(client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) { return nil, lost }, inspect: func(got client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
		return &client.RemoteFileDeleteReceipt{RemoteFileDelete: got, Revision: 20}, nil
	}}
	if err := service.WithRemoteFileDeleteMutation(t.Context(), wc, true, func() error { _, err := sendAnchorFileDelete(t.Context(), s.record, svn); return err }); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(manifest); err != nil || !bytes.Equal(before, after) {
		t.Fatal("uncertain publish advanced baseline", err)
	}
	_, snapshot, err := inspectAnchorFileDelete(t.Context(), s.record, svn)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := watcher.NewScanner(watcher.Options{WC: wc, StatePath: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.WithRemoteFileDeleteMutation(t.Context(), wc, true, func() error {
		return fresh.WithFileDeleteRecovery(t.Context(), snapshot, func() error {
			if got, _, err := inspectAnchorFileDelete(t.Context(), s.record, svn); err != nil || got == nil {
				return errors.Join(err, errors.New("missing receipt"))
			}
			return nil // Controlled filesystem/remote fixture, NOT production SVN metadata reconciliation.
		})
	}); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(manifest)
	if err != nil || bytes.Contains(base, []byte(in.Source)) {
		t.Fatal("old baseline not removed", err)
	}
	raw, err := os.ReadFile(filepath.Join(wc, in.Source))
	if err != nil || string(raw) != "new work after original deletion" || !commit.HasPendingRemoteFileDelete(wc) || svn.sends != 1 {
		t.Fatal("later work/fence lost or request replayed", err)
	}
}
