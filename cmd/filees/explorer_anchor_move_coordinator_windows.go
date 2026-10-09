//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unsafe"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"golang.org/x/sys/windows"
)

const anchorMoveCompleteName = "remote-tree.complete.json"

// Every local phase consumes an independently inspected remote receipt.
// A physical FileID, not a matching pathname, proves the chosen subtree.
func (m *anchorManager) advanceTreeMove(ctx context.Context, record localrepo.Record, bridge *anchorBridge, svn client.RemoteTreeMover, local client.LocalTreeReconciler) (*anchorMoveRecovery, error) {
	state, err := inspectAnchorTreeMoveState(ctx, record, svn)
	if err != nil {
		return nil, err
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(guards)
	in, _, _, err := readAnchorMoveIntent(ctx, record, root)
	if err != nil {
		return nil, err
	}
	proof, err := readMoveObjects(ctx, record, in)
	if err != nil {
		return nil, err
	}
	files, err := pinAnchorMoveEvidence(record, true)
	if err != nil {
		return nil, err
	}
	defer closeAnchorMoveEvidence(files)
	objectFile, _, err := openAnchorReleaseFile(filepath.Join(anchorMoveStateDir(record), anchorMoveObjectsName), false)
	if err != nil {
		return nil, err
	}
	defer objectFile.Close()
	// Pin both parents; no path supplied by recovered JSON chooses an activation.
	parents, err := pinMoveParents(record, proof)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(parents)
	if err := m.currentReleaseRecord(record); err != nil {
		return nil, err
	}
	src := filepath.Join(record.LocalPath, filepath.FromSlash(in.Move.Source))
	dst := filepath.Join(record.LocalPath, filepath.FromSlash(in.Move.Destination))
	_, srcErr := os.Lstat(src)
	_, dstErr := os.Lstat(dst)
	if srcErr == nil && os.IsNotExist(dstErr) {
		if err := m.verifyMoveObjects(ctx, record, proof, in.Move.Source, 0); err != nil {
			return nil, err
		}
		if _, err := local.LocalTreeMetadata(ctx, record.LocalPath, "inspect", in.Move, 0); err != nil {
			return nil, err
		}
		if err := bridge.connectedCommand(ctx, "tree-move", "tree-moved", in.Move.Source, in.Move.Destination, moveObjectID(proof.Objects[0].ID)); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(srcErr) || dstErr != nil {
		return nil, errors.New("tree move physical phase is ambiguous; keep evidence")
	}
	if _, err := local.LocalTreeMetadata(ctx, record.LocalPath, "stage", in.Move, state.Receipt.Revision); err != nil {
		return nil, err
	}
	// Retarget only byte-free remote identities, never convert or rewrite local
	// files. The helper accepts the old OR exact intended identity on restart.
	for _, object := range proof.Objects {
		rel := in.Move.Destination + strings.TrimPrefix(object.Path, in.Move.Source)
		h, info, err := pinMoveObject(record, rel, true)
		if err != nil {
			return nil, err
		}
		windows.CloseHandle(h)
		if anchorReleaseID(info) != object.ID {
			return nil, errors.New("tree move found a replacement object")
		}
		if object.RemoteIdentity == "" {
			continue
		}
		identity, err := anchorRemoteIdentity(rel, state.Receipt.Revision, object.Size)
		if err != nil {
			return nil, err
		}
		if err := bridge.connectedCommand(ctx, "tree-identity", "tree-identity", rel, moveObjectID(object.ID), object.RemoteIdentity, identity); err != nil {
			return nil, err
		}
	}
	if err := m.verifyMoveObjects(ctx, record, proof, in.Move.Destination, state.Receipt.Revision); err != nil {
		return nil, err
	}
	if _, err := local.LocalTreeMetadata(ctx, record.LocalPath, "ack", in.Move, state.Receipt.Revision); err != nil {
		return nil, err
	}
	if phase, err := local.LocalTreeMetadata(ctx, record.LocalPath, "verify", in.Move, state.Receipt.Revision); err != nil || phase != "complete" {
		return nil, errors.Join(err, errors.New("tree move local metadata is not complete"))
	}
	// Replace only the exact before-manifest or recognize the exact after one.
	next := movedRemoteTree(proof, state.Receipt.Revision)
	current, err := loadAnchorRemoteTree(record, root)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(current, proof.Tree) {
		if err := saveAnchorRemoteTree(record, next); err != nil {
			return nil, err
		}
	} else if !reflect.DeepEqual(current, next) {
		return nil, errors.New("tree move remote manifest has another generation")
	}
	return state, m.verifyMoveObjects(ctx, record, proof, in.Move.Destination, state.Receipt.Revision)
}

func movedRemoteTree(proof *anchorMoveObjects, revision int64) anchorRemoteTree {
	tree := proof.Tree
	tree.Nodes = append([]anchorRemoteNode(nil), tree.Nodes...)
	tree.Revision = revision
	for i, n := range tree.Nodes {
		if moveWithin(proof.Move.Source, n.Path) {
			tree.Nodes[i].Path = proof.Move.Destination + strings.TrimPrefix(n.Path, proof.Move.Source)
			size := n.Size
			if n.Kind == "dir" {
				size = 0
			}
			tree.Nodes[i].Identity, _ = anchorRemoteIdentity(tree.Nodes[i].Path, revision, size)
		}
	}
	return tree
}

// Delete a checked, opened ordinary phase record by HANDLE. Never close it and
// then unlink an unchecked pathname; a replacement must not clear the fence.
func retireMoveFile(ctx context.Context, path string, check func(*os.File) error) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_OFFLINE) != 0 {
		return errors.New("tree move phase record is not ordinary")
	}
	if err := check(f); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remove := uint32(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&remove)), 4)
}

// Called only AFTER the real Scanner's durable ACK, still inside its writer.
// Complete survives a crash before retirement. It is not a timer or inference
// from a missing receipt; recovery must repeat every final proof before ACK.
func (m *anchorManager) finishTreeMove(ctx context.Context, record localrepo.Record, svn client.RemoteTreeMover, local client.LocalTreeReconciler) error {
	if err := m.currentReleaseRecord(record); err != nil {
		return err
	}
	if repo := m.repos(record.ServerID, record.RepoID); repo == nil || repo.Summary().Access != "rw" {
		return ipcserver.ErrDepthUnavailable
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	in, _, saved, err := readAnchorMoveIntent(ctx, record, root)
	if err != nil || saved == nil {
		return errors.Join(err, errors.New("tree move completion lacks a receipt"))
	}
	state, err := inspectAnchorTreeMoveState(ctx, record, svn)
	if err != nil || state.Receipt == nil || *state.Receipt != saved.Receipt {
		return errors.Join(err, errors.New("tree move completion remote proof differs"))
	}
	proof, err := readMoveObjects(ctx, record, in)
	if err != nil {
		return err
	}
	parents, err := pinMoveParents(record, proof)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(parents)
	if err := m.verifyMoveObjects(ctx, record, proof, in.Move.Destination, saved.Receipt.Revision); err != nil {
		return err
	}
	if phase, err := local.LocalTreeMetadata(ctx, record.LocalPath, "verify", in.Move, saved.Receipt.Revision); err != nil || phase != "complete" {
		return errors.Join(err, errors.New("tree move completion metadata differs"))
	}
	current, err := loadAnchorRemoteTree(record, root)
	if err != nil || !reflect.DeepEqual(current, movedRemoteTree(proof, saved.Receipt.Revision)) {
		return errors.Join(err, errors.New("tree move completion manifest differs"))
	}
	complete := filepath.Join(anchorMoveStateDir(record), anchorMoveCompleteName)
	var old anchorMoveReceipt
	if err := readAnchorMoveJSON(ctx, complete, &old); os.IsNotExist(err) {
		if err := writeAnchorReleaseJSONFile(anchorMoveStateDir(record), anchorMoveCompleteName, *saved); err != nil {
			return err
		}
	} else if err != nil || old != *saved {
		return errors.Join(err, errors.New("tree move completion record differs"))
	}
	return retireMoveFile(ctx, filepath.Join(anchorMoveStateDir(record), commit.RemoteTreePendingName), func(f *os.File) error {
		var actual anchorMoveIntent
		if err := jsonMoveFile(f, &actual); err != nil {
			return err
		}
		if actual != in {
			return errors.New("tree move pending record changed")
		}
		return nil
	})
}

// Preview and confirmed execution share the same guarded observations. A
// preview callback must abort with its sentinel, so no scanner ACK occurs.
func (m *anchorManager) moveTree(ctx context.Context, record localrepo.Record, move client.RemoteTreeMove, consent ...func(*anchorMoveObjects, *watcher.TreeMoveSnapshot) error) error {
	return m.moveTreeWithPhysical(ctx, record, move, nil, consent...)
}

// External Windows gestures wait for the same durable remote receipt before
// their native permission is granted. OS completion precedes reconciliation.
func (m *anchorManager) moveTreeWithPhysical(ctx context.Context, record localrepo.Record, move client.RemoteTreeMove, physical func(context.Context) error, consent ...func(*anchorMoveObjects, *watcher.TreeMoveSnapshot) error) error {
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	if err := m.currentReleaseRecord(record); err != nil {
		return err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil || repo.Summary().Access != "rw" || move.RootURL != record.RepoURL {
		return ipcserver.ErrDepthUnavailable
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return err
	}
	remote, ok := svn.(client.RemoteTreeMover)
	if !ok {
		return errors.ErrUnsupported
	}
	local, ok := svn.(client.LocalTreeReconciler)
	if !ok {
		return errors.ErrUnsupported
	}
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if connection.bridge == nil || !connection.bridge.retainLocal || connection.ctx == nil || connection.ctx.Err() != nil {
		return ipcserver.ErrDepthUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.ctx, cancel)
	defer func() { stop(); cancel() }()
	return repo.WithTreeMutation(ctx, move.Source, move.Destination, nil, func(snapshot *watcher.TreeMoveSnapshot) error {
		guards, root, err := guardAnchorMoveState(record)
		if err != nil {
			return err
		}
		defer closeAnchorReleaseGuards(guards)
		if err := cleanupCompletedTreeMove(ctx, record, root); err != nil {
			return err
		}
		if err := client.ValidateRemoteTreeMove(move); err != nil {
			return err
		}
		proof, pins, err := m.captureMoveObjects(ctx, record, root, move)
		if err != nil {
			return err
		}
		defer func() { closeAnchorReleaseGuards(pins) }()
		if _, err := local.LocalTreeMetadata(ctx, record.LocalPath, "prepare", move, 0); err != nil {
			return err
		}
		if _, err := local.LocalTreeMetadata(ctx, record.LocalPath, "inspect", move, 0); err != nil {
			return err
		}
		if len(consent) > 1 {
			return errors.New("invalid tree consent")
		}
		if len(consent) == 1 {
			if err := consent[0](proof, snapshot); err != nil {
				return err
			}
		}
		if _, err := sendAnchorTreeMove(ctx, record, move, snapshot, remote, proof); err != nil {
			return err
		}
		// The remote send saw pinned original objects. Release data/list pins
		// before Windows rename (Cloud Files rejects it otherwise). From here
		// permit later local work, but prove exact FileIDs and preserve ACTUAL
		// changes; parent guards and the durable fence remain throughout.
		closeAnchorReleaseGuards(pins)
		pins = nil
		if physical != nil {
			if err := physical(ctx); err != nil {
				return err // remote receipt/fence preserved; never invent OS completion
			}
		}
		_, err = m.advanceTreeMove(ctx, record, connection.bridge, remote, local)
		return err
	}, func() error { return m.finishTreeMove(ctx, record, remote, local) })
}

// A completed marker proves ACK and retirement already happened, not that
// current user bytes are unchanged. Clean only exact metadata records. Keeping
// completion until LAST makes interrupted cleanup repeatable without replay.
func cleanupCompletedTreeMove(ctx context.Context, record localrepo.Record, root anchorReleaseFileID) error {
	dir := anchorMoveStateDir(record)
	var done anchorMoveReceipt
	err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorMoveCompleteName), &done)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateAnchorMoveIntent(done.Intent, record, root); err != nil {
		return err
	}
	if done.Intent.Schema != "filees.anchor-move-intent/v3" || done.Receipt.RemoteTreeMove != done.Intent.Move || done.Receipt.Revision <= done.Intent.Move.BaseRevision || commit.HasPendingRemoteTree(record.LocalPath) {
		return errors.New("tree move is not safely retired")
	}
	archive := "remote-tree.finished-" + done.Intent.Move.OperationID + ".json"
	var old anchorMoveReceipt
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, archive), &old); os.IsNotExist(err) {
		if err := writeAnchorReleaseJSONFile(dir, archive, done); err != nil {
			return err
		}
	} else if err != nil || old != done {
		return errors.Join(err, errors.New("tree move archive differs"))
	}
	for _, name := range []string{anchorMoveRequestName, anchorMoveReceiptName, anchorMoveScannerName, anchorMoveObjectsName, anchorMoveCompleteName} {
		err := retireMoveFile(ctx, filepath.Join(dir, name), func(f *os.File) error {
			switch name {
			case anchorMoveRequestName:
				var in anchorMoveIntent
				if err := jsonMoveFile(f, &in); err != nil {
					return err
				}
				if in != done.Intent {
					return errors.New("tree move request differs")
				}
			case anchorMoveReceiptName, anchorMoveCompleteName:
				var r anchorMoveReceipt
				if err := jsonMoveFile(f, &r); err != nil {
					return err
				}
				if r != done {
					return errors.New("tree move receipt differs")
				}
			default:
				want, limit := done.Intent.ScannerSHA256, int64(anchorMoveScannerLimit)
				if name == anchorMoveObjectsName {
					want, limit = done.Intent.ObjectsSHA256, anchorMoveObjectsLimit
				}
				stat, err := f.Stat()
				if err != nil {
					return err
				}
				if stat.Size() > limit {
					return errors.New("tree move metadata exceeds limit")
				}
				hash := sha256.New()
				if _, err := io.Copy(hash, anchorReleaseReader{ctx, io.LimitReader(f, limit+1)}); err != nil {
					return err
				}
				if hex.EncodeToString(hash.Sum(nil)) != want {
					return errors.New("tree move metadata digest differs")
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

func (m *anchorManager) recoverTreeMove(ctx context.Context, record localrepo.Record, bridge *anchorBridge) error {
	m.mu.Lock()
	connection := m.running[anchorKey(record)]
	m.mu.Unlock()
	if bridge == nil || connection.bridge != bridge || !bridge.retainLocal || bridge.root != record.LocalPath || connection.ctx == nil || connection.ctx.Err() != nil {
		return ipcserver.ErrDepthUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(connection.ctx, cancel)
	defer func() { stop(); cancel() }()
	leave, err := m.enterReleaseIntent()
	if err != nil {
		return err
	}
	defer leave()
	if err := m.currentReleaseRecord(record); err != nil {
		return err
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil || repo.Summary().Access != "rw" {
		return ipcserver.ErrDepthUnavailable
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return err
	}
	remote, ok := svn.(client.RemoteTreeMover)
	if !ok {
		return errors.ErrUnsupported
	}
	local, ok := svn.(client.LocalTreeReconciler)
	if !ok {
		return errors.ErrUnsupported
	}
	state, err := inspectAnchorTreeMoveState(ctx, record, remote)
	if err != nil {
		return err
	}
	move := state.Receipt.RemoteTreeMove
	return repo.WithTreeMutation(ctx, move.Source, move.Destination, state.Snapshot, func(snapshot *watcher.TreeMoveSnapshot) error {
		current, err := m.advanceTreeMove(ctx, record, bridge, remote, local)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.Snapshot, snapshot) {
			return errors.New("tree move recovery observations differ")
		}
		return nil
	}, func() error { return m.finishTreeMove(ctx, record, remote, local) })
}

func jsonMoveFile(f *os.File, target any) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 16<<10 {
		return fmt.Errorf("tree move phase exceeds limit")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16<<10+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("tree move phase has trailing data")
	}
	return nil
}
