//go:build windows && !nocfapi

package main

// Private retirement, not a timer/GC or production entry. Metadata remains;
// only the exact independent data.bin can be removed through its handle.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

type anchorReleaseRemoteReader interface {
	InspectRetainedRemote(context.Context, string) (client.RetainedRemoteIdentity, error)
	HistoryFetchFile(context.Context, string, int64, string) (int64, error)
}

type anchorReleaseRetirement struct {
	Version int
	Record  anchorReleaseRecord
	Copy    anchorReleaseFileID
}

const anchorReleaseBaseProofName = "base-verified.json"

// This is an ACK receipt, not another request or a clean-status snapshot.
// Caller holds the journal guard and the pinned retained handle after a
// certain bound exclusion. Never called by conditional resume or lost ACK.
func (j *anchorReleaseJournal) recordBaseProof(ctx context.Context, r anchorReleaseRecord, copy anchorReleaseFileID) error {
	if r.Phase != "exclude_requested" || r.ID != j.id || r.Identity != j.identity || copy.Index == 0 {
		return errors.New("invalid bound BASE receipt")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeAnchorReleaseJSONFile(filepath.Join(j.store, j.id), anchorReleaseBaseProofName,
		anchorReleaseRetirement{Version: 2, Record: r, Copy: copy})
}

// Missing proof is the historical remote-verification path. Present but
// invalid proof is a refusal, not permission to fall back or replace it.
func (j *anchorReleaseJournal) verifiedBase(ctx context.Context, r anchorReleaseRecord, copy anchorReleaseFileID) (bool, error) {
	proof, err := readAnchorReleaseRetirement(ctx, filepath.Join(j.store, j.id, anchorReleaseBaseProofName))
	if os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, ctx.Err()
	}
	if err != nil {
		return false, err
	}
	expected := r
	expected.Phase = "exclude_requested"
	if r.Phase != "restore_requested" || proof.Version != 2 || proof.Record != expected || proof.Copy != copy || copy.Index == 0 {
		return false, errors.New("bound BASE receipt differs; preserve retained copy")
	}
	return true, ctx.Err()
}

func readAnchorReleaseRetirement(ctx context.Context, path string) (anchorReleaseRetirement, error) {
	var out anchorReleaseRetirement
	f, info, err := openAnchorReleaseFile(path, false)
	if err != nil {
		return out, err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > 16*1024 {
		return out, errors.New("retirement evidence too large")
	}
	d := json.NewDecoder(anchorReleaseReader{ctx, io.LimitReader(f, 16*1024+1)})
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return out, errors.New("retirement evidence has trailing data")
	}
	return out, ctx.Err()
}

// The server is the activated, pinned backend, not an arbitrary download URL.
// Observe UUID before/after an immutable-revision fetch; all bytes must match.
// A deleted/renamed/unavailable remote file refuses and retains the copy.
func verifyAnchorReleaseRemote(ctx context.Context, r anchorReleaseRecord, remote anchorReleaseRemoteReader, store string) error {
	if remote == nil {
		return errors.New("retirement requires independent remote bytes")
	}
	u, err := anchorReleaseURL(r)
	if err != nil {
		return err
	}
	check := func() error {
		v, err := remote.InspectRetainedRemote(ctx, u)
		if err != nil {
			return err
		}
		if v.URL != u || v.Kind != "file" || v.RepositoryUUID != r.Identity.RepoUUID || v.Revision < r.Identity.Revision {
			return errors.New("retirement remote identity differs")
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(store, ".remote-proof-")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "remote.bin")
	// Only our two explicit temporary targets; never recursive cleanup.
	defer func() { _ = os.Remove(path); _ = os.Remove(dir) }()
	guards, err := guardAnchorReleaseDirectories(dir, "")
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	n, err := remote.HistoryFetchFile(ctx, u, r.Identity.Revision, path)
	if err != nil {
		return err
	}
	if n != r.Size {
		return errors.New("retirement remote size differs")
	}
	f, _, err := openAnchorReleaseFile(path, false)
	if err != nil {
		return err
	}
	defer f.Close()
	digest, err := hashAnchorRelease(ctx, f, nil, r.Size)
	if err != nil {
		return err
	}
	if digest != r.SHA256 {
		return errors.New("retirement remote bytes differ")
	}
	return check()
}

// Caller holds the live WC guard. Persist verified identity BEFORE deletion;
// after a crash with missing data.bin, finish the receipt only. A newly placed
// file (even identical bytes) is never removed by an old retirement request.
func (j *anchorReleaseJournal) retireCopy(ctx context.Context, ops anchorReleaseRecoveryIO, remote anchorReleaseRemoteReader) error {
	if ops.svn == nil || ops.inspect == nil {
		return errors.New("retirement needs current SVN and provider")
	}
	closeGuard, err := lockAnchorRelease(j)
	if err != nil {
		return err
	}
	defer closeGuard()
	r, err := j.inspectRecords(ctx)
	if err != nil {
		return err
	}
	if r.Phase != "restore_requested" {
		return errors.New("retirement requires a restored release")
	}
	dir := filepath.Join(j.store, j.id)
	intent, err := readAnchorReleaseRetirement(ctx, filepath.Join(dir, "04.json"))
	newIntent := os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
	if err != nil && !newIntent {
		return err
	}
	if !newIntent && (intent.Version != 1 || intent.Record != r || intent.Copy.Index == 0) {
		return errors.New("retirement evidence differs")
	}
	done, doneErr := readAnchorReleaseRetirement(ctx, filepath.Join(dir, "05.json"))
	if doneErr == nil {
		if newIntent || done != intent {
			return errors.New("retirement receipt differs")
		}
		if _, err := os.Lstat(filepath.Join(dir, "data.bin")); !os.IsNotExist(err) {
			return errors.New("completed retirement has an unexpected copy")
		}
		return ctx.Err()
	}
	if !os.IsNotExist(doneErr) && !errors.Is(doneErr, windows.ERROR_FILE_NOT_FOUND) {
		return doneErr
	}
	path := filepath.Join(dir, "data.bin")
	// Metadata admission before a data handle; no implicit hydration.
	pre, expected, err := openAnchorReleaseFile(path, false)
	if !newIntent && (os.IsNotExist(err) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return writeAnchorReleaseJSON(dir, 5, intent)
	}
	if err != nil {
		return err
	}
	pre.Close()
	wide, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.DELETE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	forbidden := uint32(windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_OFFLINE | 0x00040000 | 0x00400000)
	if anchorReleaseID(info) != anchorReleaseID(expected) || info.NumberOfLinks != 1 || info.FileAttributes&forbidden != 0 {
		return errors.New("retirement copy changed")
	}
	if !newIntent && anchorReleaseID(info) != intent.Copy {
		return errors.New("retirement refuses a replacement copy")
	}
	digest, err := hashAnchorRelease(ctx, f, nil, r.Size)
	if err != nil {
		return err
	}
	if digest != r.SHA256 {
		return errors.New("retirement copy bytes differ")
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return err
	}
	if exists, err := observeAnchorReleaseSeed(ctx, r, ops.inspect); err != nil || !exists {
		return errors.Join(err, errors.New("retirement remote seed unconfirmed"))
	}
	verified, err := j.verifiedBase(ctx, r, anchorReleaseID(info))
	if err != nil {
		return err
	}
	if !verified {
		if err := verifyAnchorReleaseRemote(ctx, r, remote, j.store); err != nil {
			return err
		}
	}
	// The byte handle and process/directory guards remain held through the
	// intent, fresh local checks, and deletion of this handle (not its name).
	if newIntent {
		intent = anchorReleaseRetirement{Version: 1, Record: r, Copy: anchorReleaseID(info)}
		if err := writeAnchorReleaseJSON(dir, 4, intent); err != nil {
			return err
		}
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return err
	}
	if exists, err := observeAnchorReleaseSeed(ctx, r, ops.inspect); err != nil || !exists {
		return errors.Join(err, errors.New("retirement remote seed changed"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deleteFlag := byte(1)
	if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &deleteFlag, 1); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return writeAnchorReleaseJSON(dir, 5, intent)
}
