//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

const anchorDeleteTreeName = "remote-file-delete.tree.json"
const anchorDeleteFinishedName = "remote-file-delete.finished.json"

type anchorDeleteFinished struct {
	Intent  anchorDeleteIntent
	Receipt anchorDeleteReceipt
}

func deleteTreeDigest(tree anchorRemoteTree) (string, error) {
	raw, err := json.Marshal(tree)
	if err != nil || len(raw) > 20<<20 {
		return "", errors.New("file deletion tree exceeds limit")
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

func readDeleteTree(ctx context.Context, record localrepo.Record, in anchorDeleteIntent) (anchorRemoteTree, *os.File, error) {
	var tree anchorRemoteTree
	f, info, err := openAnchorReleaseFile(filepath.Join(anchorMoveStateDir(record), anchorDeleteTreeName), false)
	if err != nil {
		return tree, nil, err
	}
	fail := func(err error) (anchorRemoteTree, *os.File, error) { f.Close(); return tree, nil, err }
	if info.FileSizeHigh != 0 || info.FileSizeLow > 20<<20 {
		return fail(errors.New("file deletion tree exceeds limit"))
	}
	d := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 20<<20+1)})
	d.DisallowUnknownFields()
	if err := d.Decode(&tree); err != nil {
		return fail(err)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fail(errors.New("file deletion tree has trailing data"))
	}
	digest, err := deleteTreeDigest(tree)
	if err != nil || len(in.TreeSHA256) != 64 || digest != in.TreeSHA256 ||
		tree.Schema != "filees.anchor-tree/v1" || tree.Root != in.Root || tree.URL != record.RepoURL || tree.Operation != record.OperationID || tree.Revision != in.Delete.BaseRevision {
		return fail(errors.Join(err, errors.New("file deletion manifest binding differs")))
	}
	if err := validateAnchorRemoteNodes(tree.Nodes); err != nil {
		return fail(err)
	}
	found := false
	for _, n := range tree.Nodes {
		if strings.EqualFold(n.Path, in.Delete.Source) {
			if found || n.Path != in.Delete.Source || n.Kind != "file" || n.Identity != in.Callback.Identity {
				return fail(errors.New("file deletion manifest selection differs"))
			}
			found = true
		}
	}
	if !found {
		return fail(errors.New("file deletion manifest selection missing"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return tree, f, nil
}

func deletedRemoteTree(before anchorRemoteTree, source string, revision int64) anchorRemoteTree {
	next := before
	next.Revision = revision
	next.Nodes = make([]anchorRemoteNode, 0, len(before.Nodes)-1)
	for _, n := range before.Nodes {
		if n.Path != source {
			next.Nodes = append(next.Nodes, n)
		}
	}
	return next
}

// No capabilities from a callback select a server or WC. Every entry rechecks
// the currently activated attachment and RW grant under its real writer.
func (m *anchorManager) deleteAccess(record localrepo.Record) (*ipcserver.RepoState, client.RemoteFileDeleter, client.LocalFileDeleteReconciler, error) {
	if !m.virtualEnabled(record.ServerID) {
		return nil, nil, nil, ipcserver.ErrDepthUnavailable
	}
	if err := m.currentReleaseRecord(record); err != nil {
		return nil, nil, nil, err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil || repo.Summary().Access != "rw" {
		return nil, nil, nil, ipcserver.ErrDepthUnavailable
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return nil, nil, nil, err
	}
	remote, rok := svn.(client.RemoteFileDeleter)
	local, lok := svn.(client.LocalFileDeleteReconciler)
	if !rok || !lok || !remote.RemoteFileDeletesEnabled() {
		return nil, nil, nil, errors.ErrUnsupported
	}
	return repo, remote, local, nil
}

func (m *anchorManager) prepareFileDelete(ctx context.Context, record localrepo.Record, s *anchorDeleteSession, cb anchorDeleteCallback) error {
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	repo, _, local, err := m.deleteAccess(record)
	if err != nil {
		return err
	}
	rel, base, _, valid := parseAnchorRemoteIdentity(cb.Identity)
	if !valid {
		return errors.New("file deletion has no pinned identity")
	}
	return repo.WithFileDelete(ctx, rel, nil, func(snapshot *watcher.FileDeleteSnapshot) error {
		if _, _, _, err := m.deleteAccess(record); err != nil {
			return err
		}
		guards, root, err := guardAnchorMoveState(record)
		if err != nil {
			return err
		}
		defer closeAnchorReleaseGuards(guards)
		if err := cleanupFinishedFileDelete(ctx, record, root); err != nil {
			return err
		}
		tree, err := loadAnchorRemoteTree(record, root)
		if err != nil || tree.Revision < base {
			return errors.Join(err, errors.New("file deletion requires the current manifest generation"))
		}
		found := false
		for _, n := range tree.Nodes {
			if n.Path == rel && n.Kind == "file" && n.Identity == cb.Identity {
				found = true
			}
		}
		if !found {
			return errors.New("file deletion is not a current remote manifest file")
		}
		svn, err := m.svn(record.ServerID)
		if err != nil {
			return err
		}
		reader, ok := svn.(interface {
			HistoryRepositoryUUID(context.Context, string) (string, error)
		})
		if !ok {
			return errors.ErrUnsupported
		}
		id, err := reader.HistoryRepositoryUUID(ctx, record.RepoURL)
		if err != nil {
			return err
		}
		request := client.RemoteFileDelete{RootURL: record.RepoURL, UUID: id, BaseRevision: tree.Revision, OperationID: uuid.NewString(), Source: rel, Message: "FileES: delete file"}
		localRequest := request
		localRequest.BaseRevision = base
		if _, err := local.LocalFileDeleteMetadata(ctx, record.LocalPath, "inspect", localRequest, 0); err != nil {
			return err
		}
		digest, err := deleteTreeDigest(tree)
		if err != nil {
			return err
		}
		// A crash before pending cannot authorize an OS action. A lone proof
		// is retired only by checked handle, never by a recovered pathname.
		proofPath := filepath.Join(anchorMoveStateDir(record), anchorDeleteTreeName)
		if _, err := os.Lstat(proofPath); !os.IsNotExist(err) {
			// No pending/phase file means no permission could have been sent.
			// A lone interrupted proof must be this exact bound generation.
			for _, name := range []string{commit.RemoteFileDeletePendingName, anchorDeleteCompletedName, anchorDeleteRequestedName, anchorDeleteReceiptName, anchorDeleteFinishedName} {
				if _, err := os.Lstat(filepath.Join(anchorMoveStateDir(record), name)); !os.IsNotExist(err) {
					return errors.New("file deletion has unretired evidence")
				}
			}
			if err := retireMoveFile(ctx, proofPath, func(f *os.File) error {
				var actual anchorRemoteTree
				decoder := json.NewDecoder(io.LimitReader(f, 20<<20+1))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&actual); err != nil {
					return err
				}
				var extra any
				if decoder.Decode(&extra) != io.EOF || !reflect.DeepEqual(actual, tree) {
					return errors.New("orphan file deletion tree differs")
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if err := writeAnchorReleaseJSONFileLimit(anchorMoveStateDir(record), anchorDeleteTreeName, tree, 20<<20); err != nil {
			return err
		}
		s.treeSHA256 = digest
		err = s.prepare(ctx, cb, request, snapshot, func(c context.Context) (anchorDeleteFileState, error) {
			return inspectAnchorDeleteFileState(c, m.helper, record.LocalPath, rel)
		})
		if err != nil && !commit.HasPendingRemoteFileDelete(record.LocalPath) {
			cleanupErr := retireMoveFile(ctx, proofPath, func(f *os.File) error {
				var actual anchorRemoteTree
				decoder := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 20<<20+1)})
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&actual); err != nil {
					return err
				}
				var extra any
				if decoder.Decode(&extra) != io.EOF || !reflect.DeepEqual(tree, actual) {
					return errors.New("file deletion proof changed")
				}
				return nil
			})
			return errors.Join(err, cleanupErr)
		}
		return err
	}, nil)
}

func (m *anchorManager) advanceFileDelete(ctx context.Context, record localrepo.Record, remote client.RemoteFileDeleter, local client.LocalFileDeleteReconciler) (*anchorDeleteFinished, error) {
	if _, _, _, err := m.deleteAccess(record); err != nil {
		return nil, err
	}
	in, _, _, closeState, err := pinAnchorDeleteState(ctx, record)
	if err != nil {
		return nil, err
	}
	defer closeState()
	before, proof, err := readDeleteTree(ctx, record, in)
	if err != nil {
		return nil, err
	}
	defer proof.Close()
	current, err := loadAnchorRemoteTree(record, in.Root)
	if err != nil {
		return nil, err
	}
	// An unsent COMPLETED intent may be sent once. A REQUESTED intent is
	// inspected only; even a lost reply can never initiate another commit.
	if _, err := os.Lstat(filepath.Join(anchorMoveStateDir(record), anchorDeleteRequestedName)); os.IsNotExist(err) {
		if !reflect.DeepEqual(current, before) {
			return nil, errors.New("file deletion before-manifest changed; do not send")
		}
		if _, err := sendAnchorFileDelete(ctx, record, remote); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	receipt, snapshot, err := inspectAnchorFileDelete(ctx, record, remote)
	if err != nil || !reflect.DeepEqual(snapshot, &in.Snapshot) {
		return nil, errors.Join(err, errors.New("file deletion recovery observations differ"))
	}
	next := deletedRemoteTree(before, in.Delete.Source, receipt.Revision)
	if !reflect.DeepEqual(current, before) && !reflect.DeepEqual(current, next) {
		return nil, errors.New("file deletion manifest has another generation")
	}
	for _, phase := range []string{"stage", "ack", "verify"} {
		localRequest := in.Delete
		_, localRequest.BaseRevision, _, _ = parseAnchorRemoteIdentity(in.Callback.Identity)
		state, err := local.LocalFileDeleteMetadata(ctx, record.LocalPath, phase, localRequest, receipt.Revision)
		if err != nil || (phase == "verify" && state != "complete") {
			return nil, errors.Join(err, errors.New("file deletion local metadata incomplete"))
		}
	}
	current, err = loadAnchorRemoteTree(record, in.Root)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(current, before) {
		if err := saveAnchorRemoteTree(record, next); err != nil {
			return nil, err
		}
	} else if !reflect.DeepEqual(current, next) {
		return nil, errors.New("file deletion manifest has another generation")
	}
	current, err = loadAnchorRemoteTree(record, in.Root)
	if err != nil || !reflect.DeepEqual(current, next) {
		return nil, errors.Join(err, errors.New("file deletion manifest verification differs"))
	}
	digest, _ := anchorDeleteDigest(in)
	return &anchorDeleteFinished{in, anchorDeleteReceipt{"filees.anchor-file-delete-receipt/v1", digest, *receipt}}, nil
}

func (m *anchorManager) finishFileDelete(ctx context.Context, record localrepo.Record, done *anchorDeleteFinished, remote client.RemoteFileDeleter, local client.LocalFileDeleteReconciler) error {
	if done == nil {
		return errors.New("file deletion has no completed proof")
	}
	if _, _, _, err := m.deleteAccess(record); err != nil {
		return err
	}
	verified, err := m.advanceFileDelete(ctx, record, remote, local)
	if err != nil || !reflect.DeepEqual(verified, done) {
		return errors.Join(err, errors.New("file deletion final proof differs"))
	}
	dir := anchorMoveStateDir(record)
	var old anchorDeleteFinished
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorDeleteFinishedName), &old); os.IsNotExist(err) {
		if err := writeAnchorReleaseJSONFile(dir, anchorDeleteFinishedName, done); err != nil {
			return err
		}
	} else if err != nil || !reflect.DeepEqual(old, *done) {
		return errors.Join(err, errors.New("file deletion retirement differs"))
	}
	if err := retireMoveFile(ctx, filepath.Join(dir, commit.RemoteFileDeletePendingName), func(f *os.File) error {
		var actual anchorDeleteIntent
		if err := jsonMoveFile(f, &actual); err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, done.Intent) {
			return errors.New("file deletion pending changed")
		}
		return nil
	}); err != nil {
		return err
	}
	return cleanupFinishedFileDelete(ctx, record, done.Intent.Root)
}

func cleanupFinishedFileDelete(ctx context.Context, record localrepo.Record, root anchorReleaseFileID) error {
	dir := anchorMoveStateDir(record)
	var done anchorDeleteFinished
	err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorDeleteFinishedName), &done)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateAnchorDeleteIntent(done.Intent, record, root); err != nil {
		return err
	}
	digest, err := anchorDeleteDigest(done.Intent)
	if err != nil || commit.HasPendingRemoteFileDelete(record.LocalPath) || done.Receipt.Schema != "filees.anchor-file-delete-receipt/v1" || done.Receipt.IntentSHA256 != digest || !validAnchorDeleteReceipt(done.Receipt.Receipt, done.Intent.Delete) {
		return errors.New("file deletion is not safely retired")
	}
	// Bounded fixed names, no per-operation lifetime archive. Keep finished
	// until LAST so interrupted cleanup can resume without looking at user bytes.
	for _, name := range []string{anchorDeleteRequestedName, anchorDeleteReceiptName, anchorDeleteCompletedName, anchorDeleteTreeName, anchorDeleteFinishedName} {
		err := retireMoveFile(ctx, filepath.Join(dir, name), func(f *os.File) error {
			switch name {
			case anchorDeleteRequestedName:
				var got anchorDeleteRequested
				if err := jsonMoveFile(f, &got); err != nil {
					return err
				}
				if got != (anchorDeleteRequested{"filees.anchor-file-delete-requested/v1", digest}) {
					return errors.New("file deletion request changed")
				}
			case anchorDeleteReceiptName:
				var got anchorDeleteReceipt
				if err := jsonMoveFile(f, &got); err != nil {
					return err
				}
				if got != done.Receipt {
					return errors.New("file deletion receipt changed")
				}
			case anchorDeleteCompletedName:
				var got anchorDeleteCompleted
				if err := jsonMoveFile(f, &got); err != nil {
					return err
				}
				if got.Schema != "filees.anchor-file-delete-completed/v1" || got.IntentSHA256 != digest || got.Callback.Flags != 0 || !sameAnchorDeleteObject(got.Callback, done.Intent.Callback) {
					return errors.New("file deletion OS completion changed")
				}
			case anchorDeleteTreeName:
				var got anchorRemoteTree
				decoder := json.NewDecoder(io.LimitReader(f, 20<<20+1))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&got); err != nil {
					return err
				}
				var extra any
				if decoder.Decode(&extra) != io.EOF {
					return errors.New("file deletion tree has trailing data")
				}
				hash, err := deleteTreeDigest(got)
				if err != nil || hash != done.Intent.TreeSHA256 {
					return errors.New("file deletion tree changed")
				}
			case anchorDeleteFinishedName:
				var got anchorDeleteFinished
				if err := jsonMoveFile(f, &got); err != nil {
					return err
				}
				if !reflect.DeepEqual(got, done) {
					return errors.New("file deletion finished changed")
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (m *anchorManager) recoverFileDelete(ctx context.Context, record localrepo.Record) error {
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	repo, remote, local, err := m.deleteAccess(record)
	if err != nil {
		return err
	}
	in, _, _, closeState, err := pinAnchorDeleteState(ctx, record)
	if err != nil {
		return err
	} // Missing completion NEVER becomes permission.
	closeState()
	var done *anchorDeleteFinished
	return repo.WithFileDelete(ctx, in.Delete.Source, &in.Snapshot, func(snapshot *watcher.FileDeleteSnapshot) error {
		var err error
		done, err = m.advanceFileDelete(ctx, record, remote, local)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(snapshot, &done.Intent.Snapshot) {
			return errors.New("file deletion scanner proof changed")
		}
		return nil
	}, func() error { return m.finishFileDelete(ctx, record, done, remote, local) })
}

// Bound to exactly this provider PID and connection lifetime. Close only drops
// handles; it never retires pending evidence. A successful operation permits
// a new bridge epoch only after scanner ACK and exact evidence retirement.
func (m *anchorManager) bindFileDelete(record localrepo.Record, bridge *anchorBridge, streams *anchorHelperStreams) func() {
	if !bridge.retainLocal || streams.providerPID == 0 {
		return func() {}
	}
	var mu sync.Mutex
	var s *anchorDeleteSession
	bridge.deleteRearm = true
	bridge.deletePrepare = func(ctx context.Context, cb anchorDeleteCallback) error {
		mu.Lock()
		defer mu.Unlock()
		if s != nil {
			return errors.New("file deletion session already used")
		}
		s = newAnchorDeleteSession(record, streams.providerPID)
		s.id = cb.Session
		err := m.prepareFileDelete(ctx, record, s, cb)
		if err != nil {
			m.log.Warnf("file deletion refused; evidence preserved if pending: %v", err)
			if !commit.HasPendingRemoteFileDelete(record.LocalPath) {
				s.close()
				s = nil
				return errors.Join(err, errAnchorDeleteNotPrepared)
			}
		}
		return err
	}
	bridge.deleteComplete = func(ctx context.Context, cb anchorDeleteCallback) error {
		mu.Lock()
		defer mu.Unlock()
		if s == nil {
			return errors.New("file deletion lacks its live preparation")
		}
		if err := s.complete(ctx, cb); err != nil {
			return err
		}
		s.close()
		err := m.recoverFileDelete(ctx, record)
		if err != nil {
			m.log.Warnf("file deletion recovery retained its fence: %v", err)
		} else {
			s = nil
		}
		return err
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if s != nil {
			s.close()
		}
	}
}

func (m *anchorManager) recoverConnectedFileDelete(ctx context.Context, record localrepo.Record) {
	if !commit.HasPendingRemoteFileDelete(record.LocalPath) {
		return
	}
	deadline := time.Now().Add(time.Minute)
	for ctx.Err() == nil {
		err := m.recoverFileDelete(ctx, record)
		if err == nil || ctx.Err() != nil {
			return
		}
		if (!errors.Is(err, ipcserver.ErrDepthUnavailable) && !errors.Is(err, commit.ErrLocalStatusBusy)) || time.Now().After(deadline) {
			m.log.Warnf("file deletion recovery retained its fence: %v", err)
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
