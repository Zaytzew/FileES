//go:build windows && !nocfapi

package main

// Private post-exclusion coordinator. No startup hook or user request calls
// it. It NEVER retries prepare/exclude and NEVER retires retained bytes.
import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

type anchorReleaseExclusionReader interface {
	InspectPathExclusion(context.Context, string, string) (client.PathExclusion, error)
}

type anchorReleaseRecoveryIO struct {
	svn anchorReleaseExclusionReader
	// Both callbacks address the current connected provider for j.identity.WC.
	// restore MUST be no-replace and finish/reap on cancellation before return.
	inspect func(context.Context, string) (cfapiAnswer, error)
	restore func(context.Context, string, int64) error
}

// seedObserved is a snapshot, not permission to remove the backup or a
// promise that a user cannot hydrate/edit the file immediately afterwards.
type anchorReleaseRecovery struct{ seedObserved bool }

func checkAnchorReleaseExclusion(ctx context.Context, r anchorReleaseRecord, svn anchorReleaseExclusionReader) error {
	got, err := svn.InspectPathExclusion(ctx, r.Identity.WC, r.Identity.Rel)
	if err != nil {
		return err
	}
	u, err := url.Parse(r.Identity.RepoURL)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid retained repository URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + r.Identity.Rel
	u.RawPath = ""
	if !got.Excluded || !got.Idle || got.Path != r.Identity.Rel || got.WC != filepath.ToSlash(r.Identity.WC) ||
		got.URL != u.String() || got.RepositoryUUID != r.Identity.RepoUUID || got.Revision != r.Identity.Revision {
		return errors.New("release exclusion or repository identity is not confirmed; no destructive retry")
	}
	return ctx.Err()
}

// No READ_DATA handle and no hydration. Existing ordinary files, directories,
// foreign/changed/full placeholders and older helper receipts are obstructions.
// Even a matching seed is accepted only after a recorded restore request.
func observeAnchorReleaseSeed(ctx context.Context, r anchorReleaseRecord, inspect func(context.Context, string) (cfapiAnswer, error)) (bool, error) {
	path := filepath.Join(r.Identity.WC, filepath.FromSlash(r.Identity.Rel))
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil // Parent directories are already guarded; PATH_NOT_FOUND refuses.
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return false, err
	}
	// The Cloud Files filter can return RECALL_ON_DATA_ACCESS without the
	// REPARSE_POINT bit on the opened handle (native fixture). Neither flag
	// proves ownership; the provider identity inspection below is mandatory.
	cloudFlags := uint32(windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_OFFLINE | 0x00040000 | 0x00400000)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 || (info.FileAttributes&cloudFlags == 0 && r.Size != 0) ||
		info.NumberOfLinks != 1 || int64(uint64(info.FileSizeHigh)<<32|uint64(info.FileSizeLow)) != r.Size ||
		r.Phase != "restore_requested" {
		return false, fmt.Errorf("release path is occupied; preserve local work (attributes=%x links=%d size=%d phase=%s)",
			info.FileAttributes, info.NumberOfLinks, uint64(info.FileSizeHigh)<<32|uint64(info.FileSizeLow), r.Phase)
	}
	state, err := inspect(ctx, r.Identity.Rel)
	if err != nil {
		return false, err
	}
	usn, tokenErr := strconv.ParseInt(state.USN, 10, 64)
	rel, revision, size, identityOK := parseAnchorRemoteIdentity(state.Identity)
	identityOK = identityOK && rel == r.Identity.Rel && (revision == 0 || (revision == r.Identity.Revision && size == r.Size))
	if !state.OK || !state.Placeholder || state.Local || !state.InSync || !identityOK ||
		state.Size == nil || *state.Size != r.Size || (!state.Partial && r.Size != 0) || tokenErr != nil || usn <= 0 {
		return false, errors.New("release path is not the recorded remote seed")
	}
	// Inspect by name can race a leaf replacement. Re-open attributes and
	// compare identity; this is NOT a permanent leaf reservation. No existing
	// object is ever modified, and this snapshot cannot retire the backup.
	h2, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h2)
	var after windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h2, &after); err != nil || anchorReleaseID(after) != anchorReleaseID(info) {
		return false, errors.New("release path changed during observation")
	}
	return true, ctx.Err()
}

// Caller must hold the live WC operation guard AND use the current activated
// repository identity. This private slice only repairs the missing remote seed
// after a proven exclusion. The initial coordinator binds the recorded source
// ID inside both helpers; wcOpMu alone is not that replacement fence.
func (j *anchorReleaseJournal) recoverSeed(ctx context.Context, ops anchorReleaseRecoveryIO) (anchorReleaseRecovery, error) {
	var out anchorReleaseRecovery
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := j.validate(); err != nil {
		return out, err
	}
	if ops.svn == nil || ops.inspect == nil || ops.restore == nil {
		return out, errors.New("release recovery needs explicit SVN and provider operations")
	}
	closeGuard, err := lockAnchorRelease(j)
	if err != nil {
		return out, err
	}
	defer closeGuard()
	dir := filepath.Join(j.store, j.id)
	r, err := j.inspect(ctx)
	if err != nil {
		return out, err
	}
	if r.Phase != "exclude_requested" && r.Phase != "restore_requested" {
		return out, errors.New("release has not requested exclusion; preserve evidence without replay")
	}
	// Keep byte evidence guarded throughout restoration; inspect's guard ends
	// on return, so independently hash under this new handle before admission.
	data, _, err := openAnchorReleaseFile(filepath.Join(dir, "data.bin"), false)
	if err != nil {
		return out, err
	}
	defer data.Close()
	digest, err := hashAnchorRelease(ctx, data, nil, r.Size)
	if err != nil || digest != r.SHA256 {
		return out, errors.Join(err, errors.New("release copy changed before recovery"))
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return out, err
	}
	exists, err := observeAnchorReleaseSeed(ctx, r, ops.inspect)
	if err != nil {
		return out, err
	}
	if !exists {
		if r.Phase == "exclude_requested" {
			if err := j.request(ctx, "restore_requested"); err != nil {
				return out, err
			}
			r.Phase = "restore_requested"
		}
		// Recheck after persistence, never use the old metadata snapshot as
		// authority. A newly created obstruction refuses; restore is no-replace.
		if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
			return out, err
		}
		exists, err = observeAnchorReleaseSeed(ctx, r, ops.inspect)
		if err != nil {
			return out, err
		}
		if !exists {
			restoreErr := ops.restore(ctx, r.Identity.Rel, r.Size)
			if err := ctx.Err(); err != nil {
				return out, err // Uncertain effect inspected in a fresh run, never replayed blindly.
			}
			exists, err = observeAnchorReleaseSeed(ctx, r, ops.inspect)
			if err != nil || !exists {
				return out, errors.Join(restoreErr, err, errors.New("release remote seed is not confirmed"))
			}
			// Lost ACK may coexist with a real seed: the observed effect, not
			// the callback's return value, is the proof. Copy is still retained.
		}
	}
	if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
		return out, fmt.Errorf("inspect restored exclusion: %w", err)
	}
	out.seedObserved = exists
	return out, ctx.Err()
}
