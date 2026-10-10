//go:build windows && !nocfapi

package main

// Private coordinator behind the daemon's guarded provider action/recovery.
// Caller holds wcOpMu and supplies the current activated identity/provider.
// A request is NOT a receipt.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

type anchorReleaseExcluder interface {
	anchorReleaseExclusionReader
	RequireRetainedPathSupport(context.Context) error
	ExcludeRetainedPath(context.Context, string, string, string, client.RetainedPathBinding) error
}

type anchorReleaseIO struct {
	helper  string // Exact helper used by the current connected provider.
	svn     anchorReleaseExcluder
	inspect func(context.Context, string) (cfapiAnswer, error)
	// prepare must be prepare-release-verified on the current connected helper,
	// not legacy revert. All callbacks finish/reap before returning on cancel.
	prepare func(context.Context, string, string, anchorReleaseFileID, int64, string) error
	restore func(context.Context, string, int64) error
	// Production admission persists the WC fence after retaining bytes and
	// BEFORE any mutating request. Private native probes may leave this nil.
	retained func(context.Context, *anchorReleaseJournal) error
	// Persist the restored revision identity in the tree before clearing the
	// release fence. The observer cannot race this receipt under WC/intent locks.
	remoteReceipt func(context.Context, anchorReleaseRecord) error
}

type anchorReleaseResumer interface {
	RequireRetainedResumeSupport(context.Context) error
	ResumeRetainedPath(context.Context, string, string, string, client.RetainedPathBinding) error
}

type anchorReleaseRetrySupport interface {
	RequireRetainedRetrySupport(context.Context) error
}

func anchorReleaseBinding(r anchorReleaseRecord) (client.RetainedPathBinding, error) {
	u, err := anchorReleaseURL(r)
	return client.RetainedPathBinding{SourceVolume: r.Source.Volume, SourceIndex: r.Source.Index,
		RepositoryUUID: r.Identity.RepoUUID, URL: u, Revision: r.Identity.Revision}, err
}

// Called under the process/directory guard and while retained bytes are pinned.
// A read failure is not authority: the private runtime independently checks the
// one exact pending item, repository, source and backup before doing anything.
func resumeAnchorReleaseExclusion(ctx context.Context, j *anchorReleaseJournal, r anchorReleaseRecord, svn anchorReleaseExcluder) error {
	if err := checkAnchorReleaseExclusion(ctx, r, svn); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The request was durable before the native call. A refusal before BASE
	// changed leaves no work item to resume. Re-admit only that exact ordinary
	// object and BASE; the new runtime rejects unrelated work atomically and
	// repeats source/copy/repository checks under its SVN write lock.
	if r.Phase == "exclude_requested" && checkAnchorReleaseBase(ctx, r, svn) == nil {
		retry, ok := svn.(anchorReleaseRetrySupport)
		if !ok {
			return errors.New("pre-effect release recovery unavailable; preserve retained copy")
		}
		if err := retry.RequireRetainedRetrySupport(ctx); err != nil {
			return err
		}
		if err := checkAnchorReleaseSource(ctx, r, false); err != nil {
			return err
		}
		binding, err := anchorReleaseBinding(r)
		if err != nil {
			return err
		}
		retryErr := svn.ExcludeRetainedPath(ctx, r.Identity.WC, r.Identity.Rel,
			filepath.Join(j.store, j.id, "data.bin"), binding)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkAnchorReleaseExclusion(ctx, r, svn); err != nil {
			return errors.Join(retryErr, err)
		}
		return nil
	}
	resume, ok := svn.(anchorReleaseResumer)
	if !ok {
		return errors.New("conditional release recovery unavailable; preserve retained copy")
	}
	if err := resume.RequireRetainedResumeSupport(ctx); err != nil {
		return err
	}
	binding, err := anchorReleaseBinding(r)
	if err != nil {
		return err
	}
	resumeErr := resume.ResumeRetainedPath(ctx, r.Identity.WC, r.Identity.Rel,
		filepath.Join(j.store, j.id, "data.bin"), binding)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkAnchorReleaseExclusion(ctx, r, svn); err != nil {
		return errors.Join(resumeErr, err)
	}
	return nil
}

func requireAnchorReleaseHelper(ctx context.Context, helper string) error {
	if !filepath.IsAbs(helper) || filepath.Clean(helper) != helper {
		return errors.New("release helper needs an explicit canonical path")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, "version")
	cmd.WaitDelay = time.Second
	var out anchorVersionOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := validateAnchorVersion(out.buffer.Bytes(), "dev"); err != nil {
		return err
	}
	var answer cfapiVersion
	if err := json.Unmarshal(out.buffer.Bytes(), &answer); err != nil {
		return err
	}
	for _, need := range []string{"prepare_release_verified_v1", "restore_release_v1", "release_observation_v1"} {
		found := false
		for _, feature := range answer.Features {
			if feature == need {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("CFAPI release helper lacks %s", need)
		}
	}
	return ctx.Err()
}

func lockAnchorRelease(j *anchorReleaseJournal) (func(), error) {
	if err := j.validate(); err != nil {
		return nil, err
	}
	parents, err := guardAnchorReleaseDirectories(filepath.Dir(filepath.Join(j.identity.WC, filepath.FromSlash(j.identity.Rel))), j.identity.WC)
	if err != nil {
		return nil, err
	}
	guards, err := guardAnchorReleaseDirectories(filepath.Join(j.store, j.id), "")
	if err != nil {
		closeAnchorReleaseGuards(parents)
		return nil, err
	}
	closeDirs := func() { closeAnchorReleaseGuards(guards); closeAnchorReleaseGuards(parents) }
	wide, _ := windows.UTF16PtrFromString(filepath.Join(j.store, j.id, ".resume.lock"))
	h, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		closeDirs()
		return nil, err
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &info)
	if err != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		windows.CloseHandle(h)
		closeDirs()
		return nil, errors.New("invalid release process guard")
	}
	return func() { windows.CloseHandle(h); closeDirs() }, nil
}

func anchorReleaseURL(r anchorReleaseRecord) (string, error) {
	u, err := url.Parse(r.Identity.RepoURL)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid retained repository URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + r.Identity.Rel
	u.RawPath = ""
	return u.String(), nil
}

func checkAnchorReleaseBase(ctx context.Context, r anchorReleaseRecord, svn anchorReleaseExclusionReader) error {
	got, err := svn.InspectPathExclusion(ctx, r.Identity.WC, r.Identity.Rel)
	if err != nil {
		return err
	}
	u, err := anchorReleaseURL(r)
	if err != nil {
		return err
	}
	if got.Excluded || !got.Idle || got.WC != filepath.ToSlash(r.Identity.WC) || got.Path != r.Identity.Rel ||
		got.URL != u || got.RepositoryUUID != r.Identity.RepoUUID || got.Revision != r.Identity.Revision {
		return errors.New("release BASE or repository changed; preserve retained copy")
	}
	return ctx.Err()
}

// Read only after ordinary/full-local admission. Compare the saved source ID
// and every byte; a same-content replacement is not the retained object.
func checkAnchorReleaseSource(ctx context.Context, r anchorReleaseRecord, placeholder bool) error {
	f, info, err := openAnchorReleaseFile(filepath.Join(r.Identity.WC, filepath.FromSlash(r.Identity.Rel)), placeholder)
	if err != nil {
		return err
	}
	defer f.Close()
	if anchorReleaseID(info) != r.Source {
		return errors.New("release source was replaced")
	}
	digest, err := hashAnchorRelease(ctx, f, nil, r.Size)
	if err != nil || digest != r.SHA256 {
		return errors.Join(err, errors.New("release source changed"))
	}
	return nil
}

// start retains the full file before recording any mutating request. Even
// errors return the journal where available so the caller can retain/inspect it.
func startAnchorRelease(ctx context.Context, store string, identity anchorReleaseIdentity, ops anchorReleaseIO) (*anchorReleaseJournal, anchorReleaseRecovery, error) {
	if ops.svn == nil || ops.inspect == nil || ops.prepare == nil || ops.restore == nil {
		return nil, anchorReleaseRecovery{}, errors.New("release operations unavailable")
	}
	if err := validateAnchorReleaseIdentity(identity); err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	if err := requireAnchorReleaseHelper(ctx, ops.helper); err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	if err := ops.svn.RequireRetainedPathSupport(ctx); err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	resume, ok := ops.svn.(anchorReleaseResumer)
	if !ok {
		return nil, anchorReleaseRecovery{}, errors.New("conditional release recovery unavailable")
	}
	if err := resume.RequireRetainedResumeSupport(ctx); err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	if err := checkAnchorReleaseBase(ctx, anchorReleaseRecord{Identity: identity}, ops.svn); err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	j, err := prepareAnchorReleaseJournal(ctx, store, identity, func(ctx context.Context) (cfapiAnswer, error) { return ops.inspect(ctx, identity.Rel) })
	if err != nil {
		return nil, anchorReleaseRecovery{}, err
	}
	if ops.retained != nil {
		if err := ops.retained(ctx, j); err != nil {
			return j, anchorReleaseRecovery{}, err
		}
	}
	out, err := j.advanceRelease(ctx, ops)
	return j, out, err
}

// No generic cleanup or blind destructive replay. After exclude_requested,
// recovery admits the runtime's exact conditional work item, or re-admits the
// unchanged pre-effect BASE/source with an atomic empty-queue check, followed by
// independent idle exclusion inspection and no-replace seed restoration.
func (j *anchorReleaseJournal) advanceRelease(ctx context.Context, ops anchorReleaseIO) (anchorReleaseRecovery, error) {
	var out anchorReleaseRecovery
	if ops.svn == nil || ops.inspect == nil || ops.prepare == nil || ops.restore == nil {
		return out, errors.New("release operations unavailable")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	closeGuard, err := lockAnchorRelease(j)
	if err != nil {
		return out, err
	}
	// Initial effects share the process guard; recovery takes it afresh only
	// after all callbacks have returned. No helper is left running across it.
	err = func() error {
		defer closeGuard()
		r, err := j.inspect(ctx)
		if err != nil {
			return err
		}
		if err := requireAnchorReleaseHelper(ctx, ops.helper); err != nil {
			return err
		}
		if err := ops.svn.RequireRetainedPathSupport(ctx); err != nil {
			return err
		}
		copyFile, _, err := openAnchorReleaseFile(filepath.Join(j.store, j.id, "data.bin"), false)
		if err != nil {
			return err
		}
		defer copyFile.Close()
		digest, err := hashAnchorRelease(ctx, copyFile, nil, r.Size)
		if err != nil || digest != r.SHA256 {
			return errors.Join(err, errors.New("release copy changed"))
		}
		if r.Phase == "exclude_requested" || r.Phase == "restore_requested" {
			return resumeAnchorReleaseExclusion(ctx, j, r, ops.svn)
		}
		if err := checkAnchorReleaseBase(ctx, r, ops.svn); err != nil {
			return err
		}
		if r.Phase == "retained" {
			state, err := ops.inspect(ctx, r.Identity.Rel)
			if err != nil {
				return err
			}
			retainedState := state
			retainedState.USN = r.USN
			if !anchorRetainedMetadataCompatible(retainedState, state, r.Size) || state.Identity != "filees.local/v1" {
				return errors.New("retained FileES source changed before preparation")
			}
			if err := checkAnchorReleaseSource(ctx, r, true); err != nil {
				return err
			}
			if err := j.request(ctx, "prepare_requested"); err != nil {
				return err
			}
			r.Phase = "prepare_requested"
			// The retained token remains immutable evidence. The connected helper
			// independently hashes this same FileID under a share-none handle, and
			// permits later USN only with exact size/digest and clean CFAPI flags.
			prepareErr := ops.prepare(ctx, r.Identity.Rel, r.USN, r.Source, r.Size, r.SHA256)
			if err := ctx.Err(); err != nil {
				return err
			}
			// A lost ACK is resolved by the actual ordinary file. A remaining
			// placeholder is a refusal, not permission to repeat preparation.
			if err := checkAnchorReleaseSource(ctx, r, false); err != nil {
				return errors.Join(prepareErr, err)
			}
		}
		if r.Phase != "prepare_requested" {
			return errors.New("unsupported release phase")
		}
		prepared, err := ops.inspect(ctx, r.Identity.Rel)
		if err != nil || !prepared.OK || prepared.Placeholder || prepared.Partial {
			return errors.Join(err, errors.New("source preparation is not confirmed by Cloud Files metadata"))
		}
		if err := checkAnchorReleaseSource(ctx, r, false); err != nil {
			return err
		}
		if err := checkAnchorReleaseBase(ctx, r, ops.svn); err != nil {
			return err
		}
		if err := j.request(ctx, "exclude_requested"); err != nil {
			return err
		}
		binding, err := anchorReleaseBinding(r)
		if err != nil {
			return err
		}
		// Helper binds its acquired source handle to this record, and runtime
		// repeats repo/revision checks UNDER its SVN write lock. This earlier
		// Go snapshot alone does not authorize a pathname deletion.
		excludeErr := ops.svn.ExcludeRetainedPath(ctx, r.Identity.WC, r.Identity.Rel, copyFile.Name(), binding)
		if err := ctx.Err(); err != nil {
			return err
		}
		// Ignore only an uncertain receipt whose complete idle effect is
		// independently proven, never an unfinished work queue.
		if err := checkAnchorReleaseExclusion(ctx, r, ops.svn); err != nil {
			return errors.Join(excludeErr, err)
		}
		return nil
	}()
	if err != nil {
		return out, fmt.Errorf("release retained for inspection: %w", err)
	}
	return j.recoverSeed(ctx, anchorReleaseRecoveryIO{svn: ops.svn, inspect: ops.inspect, restore: ops.restore})
}
