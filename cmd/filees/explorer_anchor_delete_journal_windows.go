//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/watcher"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

// Internal prerequisite only: no production delete callback is enabled here.
// The coordinator must hold the real WC writer/scanner, authenticate current
// permission and prove this exact manifest node before calling preparation.
// Session is supplied by the owning live bridge, never trusted from a frame.
type anchorDeleteCallback struct {
	Session                  string
	Volume                   uint32
	RootFileID, FileID       uint64
	RequestKey               int64
	Flags, ProcessID         uint32
	Identity, NormalizedPath string
}

type anchorDeleteIntent struct {
	Schema, ServerID, RepoID, AttachmentID, WC string
	ProviderPID                                uint32
	Root                                       anchorReleaseFileID
	Callback                                   anchorDeleteCallback
	Delete                                     client.RemoteFileDelete
	Snapshot                                   watcher.FileDeleteSnapshot
	Parents                                    []anchorMoveObject
}

type anchorDeleteCompleted struct {
	Schema, IntentSHA256 string
	Callback             anchorDeleteCallback
}

type anchorDeleteRequested struct{ Schema, IntentSHA256 string }
type anchorDeleteReceipt struct {
	Schema, IntentSHA256 string
	Receipt              client.RemoteFileDeleteReceipt
}

const anchorDeleteCompletedName = "remote-file-delete.completed.json"
const anchorDeleteRequestedName = "remote-file-delete.requested.json"
const anchorDeleteReceiptName = "remote-file-delete.receipt.json"

// One approved object per live connection. A fresh connection cannot complete
// an old prepared operation, even with identical PID/path/RequestKey. Close
// releases handles but NEVER removes its pending fence or fabricates completion.
type anchorDeleteSession struct {
	mu                sync.Mutex
	record            localrepo.Record
	id                string
	providerPID       uint32
	in                *anchorDeleteIntent
	guards            []windows.Handle
	object            windows.Handle
	evidence          *os.File
	closed, completed bool
}

func newAnchorDeleteSession(record localrepo.Record, providerPID uint32) *anchorDeleteSession {
	return &anchorDeleteSession{record: record, id: uuid.NewString(), providerPID: providerPID, object: windows.InvalidHandle}
}

func (s *anchorDeleteSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.object != windows.InvalidHandle {
		windows.CloseHandle(s.object)
		s.object = windows.InvalidHandle
	}
	if s.evidence != nil {
		s.evidence.Close()
		s.evidence = nil
	}
	closeAnchorReleaseGuards(s.guards)
	s.guards = nil
}

func anchorDeleteParents(source string) []string {
	var out []string
	for p := path.Dir(source); ; p = path.Dir(p) {
		out = append(out, p)
		if p == "." {
			return out
		}
	}
}

func anchorDeleteDigest(in anchorDeleteIntent) (string, error) {
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 16*1024 {
		return "", errors.New("file deletion evidence exceeds limit")
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

func sameAnchorDeleteObject(a, b anchorDeleteCallback) bool {
	// RequestKey is intentionally excluded: pre can repeat, completion is 0 on
	// the measured Windows. Flags are phase-specific and checked separately.
	return a.Session == b.Session && a.Volume == b.Volume && a.RootFileID == b.RootFileID && a.FileID == b.FileID &&
		a.ProcessID == b.ProcessID && a.Identity == b.Identity && a.NormalizedPath == b.NormalizedPath
}

func validateAnchorDeleteIntent(in anchorDeleteIntent, record localrepo.Record, root anchorReleaseFileID) error {
	cb := in.Callback
	session, err := uuid.Parse(cb.Session)
	full := filepath.Join(record.LocalPath, filepath.FromSlash(in.Delete.Source))
	volume := filepath.VolumeName(record.LocalPath)
	if in.Schema != "filees.anchor-file-delete-intent/v1" || in.ServerID == "" || in.ServerID != record.ServerID || in.RepoID == "" || in.RepoID != record.RepoID ||
		in.AttachmentID == "" || in.AttachmentID != record.OperationID || in.WC != record.LocalPath || !filepath.IsAbs(in.WC) || filepath.Clean(in.WC) != in.WC ||
		in.Root != root || root.Index == 0 || cb.Volume != root.Volume || cb.RootFileID != root.Index || cb.FileID == 0 || cb.FileID == root.Index ||
		in.ProviderPID == 0 || cb.ProcessID == 0 || cb.ProcessID == in.ProviderPID || cb.Flags != 0 || cb.RequestKey == 0 ||
		err != nil || session == uuid.Nil || session.String() != cb.Session || len(volume) != 2 || volume[1] != ':' ||
		!strings.EqualFold(cb.NormalizedPath, strings.TrimPrefix(full, volume)) || in.Delete.RootURL != record.RepoURL {
		return errors.New("file deletion differs from its live object or activated attachment")
	}
	if err := client.ValidateRemoteFileDelete(in.Delete); err != nil {
		return err
	}
	rel, revision, _, ok := parseAnchorRemoteIdentity(cb.Identity)
	if !ok || rel != in.Delete.Source || revision != in.Delete.BaseRevision {
		return errors.New("file deletion lacks an exact remote identity")
	}
	if err := watcher.ValidateFileDeleteSnapshot(&in.Snapshot, in.WC); err != nil {
		return err
	}
	if in.Snapshot.Source != in.Delete.Source {
		return errors.New("file deletion scanner selection differs")
	}
	parents := anchorDeleteParents(in.Delete.Source)
	if len(parents) != len(in.Parents) {
		return errors.New("file deletion parent evidence incomplete")
	}
	for i, p := range in.Parents {
		if p.Path != parents[i] || !p.Directory || p.Size != 0 || p.RemoteIdentity != "" || p.ID.Index == 0 || p.ID.Volume != root.Volume || (p.Path == "." && p.ID != root) {
			return errors.New("file deletion parent evidence differs")
		}
	}
	_, err = anchorDeleteDigest(in)
	return err
}

func (s *anchorDeleteSession) prepare(ctx context.Context, cb anchorDeleteCallback, request client.RemoteFileDelete, snapshot *watcher.FileDeleteSnapshot, inspect func(context.Context) (cfapiAnswer, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.completed || cb.Session != s.id || inspect == nil || snapshot == nil {
		return errors.New("file deletion session unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Bound selection before opening ancestors or allocating phase evidence.
	if err := client.ValidateRemoteFileDelete(request); err != nil {
		return err
	}
	if err := watcher.ValidateFileDeleteSnapshot(snapshot, s.record.LocalPath); err != nil {
		return err
	}
	if s.in != nil {
		if !sameAnchorDeleteObject(s.in.Callback, cb) || cb.Flags != 0 || cb.RequestKey == 0 || s.in.Delete != request || s.in.Snapshot != *snapshot {
			return errors.New("another deletion or changed object is already prepared")
		}
		return nil // Repeated pre for the SAME pinned object is not a second intent.
	}
	guards, root, err := guardAnchorMoveState(s.record)
	if err != nil {
		return err
	}
	object := windows.InvalidHandle
	retained := false
	defer func() {
		if !retained {
			closeAnchorReleaseGuards(guards)
			if object != windows.InvalidHandle {
				windows.CloseHandle(object)
			}
		}
	}()
	in := anchorDeleteIntent{Schema: "filees.anchor-file-delete-intent/v1", ServerID: s.record.ServerID, RepoID: s.record.RepoID,
		AttachmentID: s.record.OperationID, WC: s.record.LocalPath, Root: root, ProviderPID: s.providerPID, Callback: cb, Delete: request, Snapshot: *snapshot}
	for _, parent := range anchorDeleteParents(request.Source) {
		pins, err := guardAnchorReleaseDirectories(filepath.Join(s.record.LocalPath, filepath.FromSlash(parent)), s.record.LocalPath)
		if err != nil {
			return err
		}
		guards = append(guards, pins...)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(pins[len(pins)-1], &info); err != nil {
			return err
		}
		in.Parents = append(in.Parents, anchorMoveObject{Path: parent, ID: anchorReleaseID(info), Directory: true})
	}
	if err := validateAnchorDeleteIntent(in, s.record, root); err != nil {
		return err
	}
	state, err := inspect(ctx)
	if err != nil {
		return err
	}
	if !anchorDeleteRemoteState(state, cb.Identity) {
		return errors.New("file deletion requires a clean nonresident FileES file")
	}
	var info windows.ByHandleFileInformation
	object, info, err = pinMoveObject(s.record, request.Source, true)
	if err != nil {
		return err
	}
	if anchorReleaseID(info) != (anchorReleaseFileID{cb.Volume, cb.FileID}) || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return errors.New("file deletion object was replaced")
	}
	checked, err := inspect(ctx)
	if err != nil || !anchorDeleteRemoteState(checked, cb.Identity) || checked.USN != state.USN {
		return errors.Join(err, errors.New("file deletion metadata changed before preparation"))
	}
	dir := anchorMoveStateDir(s.record)
	for _, name := range []string{commit.FileReleasePendingName, commit.RemoteTreePendingName, commit.RemoteFileDeletePendingName, anchorDeleteCompletedName, anchorDeleteRequestedName, anchorDeleteReceiptName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return errors.New("file deletion has pending or unreadable evidence")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAnchorReleaseJSONFile(dir, commit.RemoteFileDeletePendingName, in); err != nil {
		return err
	}
	f, _, err := openAnchorReleaseFile(filepath.Join(dir, commit.RemoteFileDeletePendingName), false)
	if err != nil {
		return err
	} // Fence remains, but no ACK permission is returned.
	var saved anchorDeleteIntent
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, commit.RemoteFileDeletePendingName), &saved); err != nil {
		f.Close()
		return err
	}
	a, _ := anchorDeleteDigest(in)
	b, _ := anchorDeleteDigest(saved)
	if a != b {
		f.Close()
		return errors.New("file deletion evidence changed before ACK")
	}
	s.in, s.guards, s.object, s.evidence = &in, guards, object, f
	retained = true
	return nil // Only now may a future callback coordinator grant ACK_DELETE.
}

func anchorDeleteRemoteState(s cfapiAnswer, identity string) bool {
	_, _, size, valid := parseAnchorRemoteIdentity(identity)
	usn, err := strconv.ParseInt(s.USN, 10, 64)
	return valid && s.OK && s.Placeholder && !s.Directory && !s.Local && !s.Partial && s.InSync && s.OnDisk == 0 && s.Modified == 0 &&
		s.Identity == identity && s.Size != nil && *s.Size == size && err == nil && usn > 0 && strconv.FormatInt(usn, 10) == s.USN
}

func (s *anchorDeleteSession) complete(ctx context.Context, cb anchorDeleteCallback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.in == nil || !sameAnchorDeleteObject(s.in.Callback, cb) || cb.Flags != 0 {
		return errors.New("unrelated or lost file deletion completion")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.completed {
		return nil
	}
	// Closing the old, share-delete handle may finish Windows' deletion. It
	// never operates by name, and cannot close/delete a later replacement.
	if s.object != windows.InvalidHandle {
		windows.CloseHandle(s.object)
		s.object = windows.InvalidHandle
	}
	h, info, err := pinMoveObject(s.record, s.in.Delete.Source, true)
	if err == nil {
		windows.CloseHandle(h)
		if anchorReleaseID(info) == (anchorReleaseFileID{cb.Volume, cb.FileID}) {
			return errors.New("completed deletion still has its original object")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	digest, err := anchorDeleteDigest(*s.in)
	if err != nil {
		return err
	}
	if err := writeAnchorReleaseJSONFile(anchorMoveStateDir(s.record), anchorDeleteCompletedName,
		anchorDeleteCompleted{"filees.anchor-file-delete-completed/v1", digest, cb}); err != nil {
		return err
	}
	s.completed = true
	return nil
}

// read/pin checks current root and every persisted parent before consulting
// SVN. Evidence remains pinned through send/inspection, including phase files.
func pinAnchorDeleteState(ctx context.Context, record localrepo.Record) (anchorDeleteIntent, string, *anchorDeleteReceipt, func(), error) {
	var in anchorDeleteIntent
	guards, root, err := guardAnchorMoveState(record)
	var files []*os.File
	close := func() { closeAnchorMoveEvidence(files); closeAnchorReleaseGuards(guards) }
	fail := func(err error) (anchorDeleteIntent, string, *anchorDeleteReceipt, func(), error) {
		close()
		return in, "", nil, func() {}, err
	}
	if err != nil {
		return fail(err)
	}
	dir := anchorMoveStateDir(record)
	for _, name := range []string{commit.RemoteFileDeletePendingName, anchorDeleteCompletedName} {
		f, _, err := openAnchorReleaseFile(filepath.Join(dir, name), false)
		if err != nil {
			return fail(err)
		}
		files = append(files, f)
	}
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, commit.RemoteFileDeletePendingName), &in); err != nil {
		return fail(err)
	}
	if err := validateAnchorDeleteIntent(in, record, root); err != nil {
		return fail(err)
	}
	digest, err := anchorDeleteDigest(in)
	if err != nil {
		return fail(err)
	}
	var completed anchorDeleteCompleted
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorDeleteCompletedName), &completed); err != nil {
		return fail(err)
	}
	if completed.Schema != "filees.anchor-file-delete-completed/v1" || completed.IntentSHA256 != digest || completed.Callback.Flags != 0 || !sameAnchorDeleteObject(in.Callback, completed.Callback) {
		return fail(errors.New("file deletion lacks its bound successful OS completion"))
	}
	for _, parent := range in.Parents {
		pins, err := guardAnchorReleaseDirectories(filepath.Join(record.LocalPath, filepath.FromSlash(parent.Path)), record.LocalPath)
		if err != nil {
			return fail(err)
		}
		guards = append(guards, pins...)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(pins[len(pins)-1], &info); err != nil || anchorReleaseID(info) != parent.ID {
			return fail(errors.New("file deletion parent was replaced"))
		}
	}
	var saved *anchorDeleteReceipt
	for _, name := range []string{anchorDeleteRequestedName, anchorDeleteReceiptName} {
		f, _, err := openAnchorReleaseFile(filepath.Join(dir, name), false)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		files = append(files, f)
		if name == anchorDeleteRequestedName {
			var requested anchorDeleteRequested
			if err := readAnchorMoveJSON(ctx, filepath.Join(dir, name), &requested); err != nil {
				return fail(err)
			}
			if requested != (anchorDeleteRequested{"filees.anchor-file-delete-requested/v1", digest}) {
				return fail(errors.New("file deletion request differs"))
			}
		} else {
			saved = &anchorDeleteReceipt{}
			if err := readAnchorMoveJSON(ctx, filepath.Join(dir, name), saved); err != nil {
				return fail(err)
			}
			if saved.Schema != "filees.anchor-file-delete-receipt/v1" || saved.IntentSHA256 != digest || !validAnchorDeleteReceipt(saved.Receipt, in.Delete) {
				return fail(errors.New("file deletion receipt differs"))
			}
		}
	}
	if hasPendingOtherDeleteMutation(record.LocalPath) {
		return fail(errors.New("file deletion has another pending mutation"))
	}
	return in, digest, saved, close, nil
}

func hasPendingOtherDeleteMutation(wc string) bool {
	return commit.HasPendingRemoteTree(wc) || commit.HasPendingFileRelease(wc)
}

func validAnchorDeleteReceipt(r client.RemoteFileDeleteReceipt, in client.RemoteFileDelete) bool {
	return r.RemoteFileDelete == in && r.Revision > in.BaseRevision && r.Revision <= math.MaxInt32
}

// Caller owns real writer/current authorization. No receipt clears the fence;
// manifest/SVN/scanner reconciliation and exact retirement remain mandatory.
func sendAnchorFileDelete(ctx context.Context, record localrepo.Record, svn client.RemoteFileDeleter) (*client.RemoteFileDeleteReceipt, error) {
	if svn == nil || !svn.RemoteFileDeletesEnabled() {
		return nil, errors.New("native file deletion unavailable")
	}
	in, digest, saved, close, err := pinAnchorDeleteState(ctx, record)
	if err != nil {
		return nil, err
	}
	defer close()
	dir := anchorMoveStateDir(record)
	if _, err := os.Lstat(filepath.Join(dir, anchorDeleteRequestedName)); !os.IsNotExist(err) || saved != nil {
		return nil, errors.New("file deletion already requested; inspect, never replay")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, anchorDeleteRequestedName, anchorDeleteRequested{"filees.anchor-file-delete-requested/v1", digest}); err != nil {
		return nil, err
	}
	f, _, err := openAnchorReleaseFile(filepath.Join(dir, anchorDeleteRequestedName), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var requested anchorDeleteRequested
	if err := readAnchorMoveJSON(ctx, filepath.Join(dir, anchorDeleteRequestedName), &requested); err != nil {
		return nil, err
	}
	if requested != (anchorDeleteRequested{"filees.anchor-file-delete-requested/v1", digest}) {
		return nil, errors.New("file deletion request changed before send")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	receipt, err := svn.RemoteFileDelete(ctx, in.Delete)
	if err != nil {
		return nil, err
	} // Even not-sent retains intent; no automatic replay.
	if receipt == nil || !validAnchorDeleteReceipt(*receipt, in.Delete) {
		return nil, errors.New("file deletion returned an unrelated receipt")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAnchorReleaseJSONFile(dir, anchorDeleteReceiptName, anchorDeleteReceipt{"filees.anchor-file-delete-receipt/v1", digest, *receipt}); err != nil {
		return nil, err
	}
	return receipt, nil
}

func inspectAnchorFileDelete(ctx context.Context, record localrepo.Record, svn client.RemoteFileDeleter) (*client.RemoteFileDeleteReceipt, *watcher.FileDeleteSnapshot, error) {
	if svn == nil || !svn.RemoteFileDeletesEnabled() {
		return nil, nil, errors.New("native file deletion inspection unavailable")
	}
	in, digest, saved, close, err := pinAnchorDeleteState(ctx, record)
	if err != nil {
		return nil, nil, err
	}
	defer close()
	if _, err := os.Lstat(filepath.Join(anchorMoveStateDir(record), anchorDeleteRequestedName)); err != nil {
		return nil, nil, errors.New("file deletion was not requested; preserve evidence")
	}
	receipt, err := svn.InspectRemoteFileDelete(ctx, in.Delete)
	if err != nil {
		return nil, nil, err
	}
	if receipt == nil || !validAnchorDeleteReceipt(*receipt, in.Delete) || (saved != nil && saved.Receipt != *receipt) {
		return nil, nil, errors.New("file deletion has no exact remote receipt; do not replay")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if saved == nil {
		if err := writeAnchorReleaseJSONFile(anchorMoveStateDir(record), anchorDeleteReceiptName, anchorDeleteReceipt{"filees.anchor-file-delete-receipt/v1", digest, *receipt}); err != nil {
			return nil, nil, err
		}
	}
	return receipt, &in.Snapshot, nil
}
