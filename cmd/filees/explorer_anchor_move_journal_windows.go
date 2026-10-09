//go:build windows && !nocfapi

package main

// Private prerequisite, not an IPC action/startup mutator. Caller holds the
// real WC writer and scanner/local selection guards for the entire callback.
// Recovery is READ-ONLY on SVN, never a retry or permission to retire evidence.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"golang.org/x/sys/windows"
)

type anchorMoveIntent struct {
	Schema                             string
	ServerID, RepoID, AttachmentID, WC string
	Root                               anchorReleaseFileID
	Move                               client.RemoteTreeMove
	ScannerSHA256                      string
}

type anchorMoveReceipt struct {
	Intent  anchorMoveIntent
	Receipt client.RemoteTreeReceipt
}

const anchorMoveRequestName = "remote-tree.requested.json"
const anchorMoveReceiptName = "remote-tree.receipt.json"
const anchorMoveScannerName = "remote-tree.scanner.json"
const anchorMoveScannerLimit = 64 << 20

func anchorMoveStateDir(record localrepo.Record) string {
	return filepath.Join(record.LocalPath, ".filees", "state")
}

func validateAnchorMoveIntent(in anchorMoveIntent, record localrepo.Record, root anchorReleaseFileID) error {
	if in.Schema != "filees.anchor-move-intent/v2" || in.ServerID == "" || in.RepoID == "" || in.AttachmentID == "" ||
		in.ServerID != record.ServerID || in.RepoID != record.RepoID || in.AttachmentID != record.OperationID ||
		in.WC != record.LocalPath || in.Move.RootURL != record.RepoURL || in.Root != root || root.Index == 0 ||
		!filepath.IsAbs(in.WC) || filepath.Clean(in.WC) != in.WC {
		return errors.New("tree move differs from the current activated attachment")
	}
	digest, err := hex.DecodeString(in.ScannerSHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != in.ScannerSHA256 {
		return errors.New("tree move lacks its immutable scanner digest")
	}
	return client.ValidateRemoteTreeMove(in.Move)
}

// Directory handles prevent root/parent replacement. Never use a recovered
// path to choose the WC; it comes from the currently activated lifecycle record.
func guardAnchorMoveState(record localrepo.Record) ([]windows.Handle, anchorReleaseFileID, error) {
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return nil, anchorReleaseFileID{}, err
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(guards[len(guards)-1], &info)
	if err != nil {
		closeAnchorReleaseGuards(guards)
		return nil, anchorReleaseFileID{}, err
	}
	state, err := guardAnchorReleaseDirectories(anchorMoveStateDir(record), record.LocalPath)
	if err != nil {
		closeAnchorReleaseGuards(guards)
		return nil, anchorReleaseFileID{}, err
	}
	return append(guards, state...), anchorReleaseID(info), nil
}

func readAnchorMoveJSON(ctx context.Context, path string, target any) error {
	f, info, err := openAnchorReleaseFile(path, false)
	if err != nil {
		return err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > 16*1024 {
		return errors.New("tree move evidence exceeds size limit")
	}
	d := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 16*1024+1)})
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("tree move evidence has trailing data")
	}
	return ctx.Err()
}

// Ordinary evidence handles deny write/delete while consulting SVN. Directory
// guards alone prevent root replacement, not replacement of these phase files.
func pinAnchorMoveEvidence(record localrepo.Record, saved bool) ([]*os.File, error) {
	names := []string{commit.RemoteTreePendingName, anchorMoveRequestName, anchorMoveScannerName}
	if saved {
		names = append(names, anchorMoveReceiptName)
	}
	var files []*os.File
	for _, name := range names {
		f, _, err := openAnchorReleaseFile(filepath.Join(anchorMoveStateDir(record), name), false)
		if err != nil {
			closeAnchorMoveEvidence(files)
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

func closeAnchorMoveEvidence(files []*os.File) {
	for i := len(files) - 1; i >= 0; i-- {
		_ = files[i].Close()
	}
}

func readAnchorMoveIntent(ctx context.Context, record localrepo.Record, root anchorReleaseFileID) (anchorMoveIntent, bool, *anchorMoveReceipt, error) {
	var in, requested anchorMoveIntent
	dir := anchorMoveStateDir(record)
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, commit.RemoteTreePendingName), &in); err != nil {
		return in, false, nil, err
	}
	if err := validateAnchorMoveIntent(in, record, root); err != nil {
		return in, false, nil, err
	}
	err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorMoveRequestName), &requested)
	sent := err == nil
	if err != nil && !os.IsNotExist(err) {
		return in, false, nil, err
	}
	if sent && requested != in {
		return in, false, nil, errors.New("tree move request differs from its immutable intent")
	}
	var receipt anchorMoveReceipt
	err = readAnchorMoveJSON(ctx, filepath.Join(dir, anchorMoveReceiptName), &receipt)
	if os.IsNotExist(err) {
		return in, sent, nil, nil
	}
	if err != nil {
		return in, sent, nil, err
	}
	if !sent || receipt.Intent != in || receipt.Receipt.RemoteTreeMove != in.Move || receipt.Receipt.Revision <= in.Move.BaseRevision {
		return in, sent, nil, errors.New("tree move receipt is unrelated or lacks a request")
	}
	return in, sent, &receipt, nil
}

// prepare/send is one internal callback under Service.WithRemoteTreeMove
// and a future exact local subtree guard. Every marker precedes its
// external effect. Once created, no marker is replaced, deleted or timed out.
func sendAnchorTreeMove(ctx context.Context, record localrepo.Record, move client.RemoteTreeMove, snapshot *watcher.TreeMoveSnapshot, svn client.RemoteTreeMover) (*client.RemoteTreeReceipt, error) {
	if svn == nil || !svn.RemoteTreeMovesEnabled() {
		return nil, errors.New("native tree move unavailable")
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(guards)
	digest, err := anchorMoveSnapshotDigest(snapshot, record, move)
	if err != nil {
		return nil, err
	}
	in := anchorMoveIntent{Schema: "filees.anchor-move-intent/v2", ServerID: record.ServerID, RepoID: record.RepoID,
		AttachmentID: record.OperationID, WC: record.LocalPath, Root: root, Move: move, ScannerSHA256: digest}
	if err := validateAnchorMoveIntent(in, record, root); err != nil {
		return nil, err
	}
	dir := anchorMoveStateDir(record)
	for _, name := range []string{commit.FileReleasePendingName, commit.RemoteTreePendingName, anchorMoveRequestName, anchorMoveReceiptName, anchorMoveScannerName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return nil, errors.New("tree move has pending or unreadable evidence; inspect, do not replay")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, commit.RemoteTreePendingName, in); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFileLimit(dir, anchorMoveScannerName, snapshot, anchorMoveScannerLimit); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, anchorMoveRequestName, in); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := pinAnchorMoveEvidence(record, false)
	if err != nil {
		return nil, err
	}
	defer closeAnchorMoveEvidence(files)
	checked, sent, saved, err := readAnchorMoveIntent(ctx, record, root)
	if err != nil || checked != in || !sent || saved != nil {
		return nil, errors.Join(err, errors.New("tree move evidence changed before send"))
	}
	if _, err := readAnchorMoveSnapshot(ctx, record, in); err != nil {
		return nil, err
	}
	receipt, err := svn.RemoteTreeMove(ctx, move)
	if err != nil {
		return nil, err
	} // Even commit-not-sent leaves evidence for explicit recovery.
	if receipt == nil || receipt.RemoteTreeMove != move || receipt.Revision <= move.BaseRevision {
		return nil, errors.New("tree move returned an unrelated receipt; inspect before any retry")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, anchorMoveReceiptName, anchorMoveReceipt{in, *receipt}); err != nil {
		return nil, err
	}
	return receipt, nil // Local reconciliation remains fenced even on success.
}

// Caller uses the recovery branch of the same real writer guard. This does
// not prove local reconciliation, clear the fence or resurrect an old writer.
// A stored receipt is compared to remote evidence, never trusted on its own.
func inspectAnchorTreeMove(ctx context.Context, record localrepo.Record, svn client.RemoteTreeMover) (*client.RemoteTreeReceipt, error) {
	state, err := inspectAnchorTreeMoveState(ctx, record, svn)
	if err != nil {
		return nil, err
	}
	return state.Receipt, nil
}

type anchorMoveRecovery struct {
	Receipt  *client.RemoteTreeReceipt
	Snapshot *watcher.TreeMoveSnapshot
}

// The future local coordinator consumes exactly these persisted observations.
// A confirmed remote receipt alone never authorizes scanner ACK or retirement.
func inspectAnchorTreeMoveState(ctx context.Context, record localrepo.Record, svn client.RemoteTreeMover) (*anchorMoveRecovery, error) {
	if svn == nil || !svn.RemoteTreeMovesEnabled() {
		return nil, errors.New("native tree move inspection unavailable")
	}
	guards, root, err := guardAnchorMoveState(record)
	if err != nil {
		return nil, err
	}
	defer closeAnchorReleaseGuards(guards)
	in, requested, saved, err := readAnchorMoveIntent(ctx, record, root)
	if err != nil {
		return nil, err
	}
	if !requested {
		return nil, errors.New("tree move was prepared but not requested; preserve evidence")
	}
	files, err := pinAnchorMoveEvidence(record, saved != nil)
	if err != nil {
		return nil, err
	}
	defer closeAnchorMoveEvidence(files)
	checked, sent, pinnedReceipt, err := readAnchorMoveIntent(ctx, record, root)
	if err != nil || checked != in || !sent || (saved == nil) != (pinnedReceipt == nil) || (saved != nil && *saved != *pinnedReceipt) {
		return nil, errors.Join(err, errors.New("tree move evidence changed before inspection"))
	}
	snapshot, err := readAnchorMoveSnapshot(ctx, record, in)
	if err != nil {
		return nil, err
	}
	receipt, err := svn.InspectRemoteTreeMove(ctx, in.Move)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, errors.New("tree move has no confirmed remote receipt; preserve evidence, do not replay")
	}
	if receipt.RemoteTreeMove != in.Move || receipt.Revision <= in.Move.BaseRevision || (saved != nil && saved.Receipt != *receipt) {
		return nil, errors.New("tree move remote evidence differs from its bound request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if saved == nil {
		if err := writeAnchorReleaseJSONFile(anchorMoveStateDir(record), anchorMoveReceiptName, anchorMoveReceipt{in, *receipt}); err != nil {
			return nil, err
		}
	}
	return &anchorMoveRecovery{Receipt: receipt, Snapshot: snapshot}, nil
}
