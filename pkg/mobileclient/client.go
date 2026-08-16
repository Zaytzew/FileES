package mobileclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
)

// Transport carries one framed mobile operation to the server and returns the
// response. Implementations wrap an SSH session (one operation per session); the
// core stays transport-neutral so it can be tested against an in-process worker.
type Transport interface {
	Do(ctx context.Context, req v1.Request, reqPayload []byte) (resp v1.Response, respPayload []byte, err error)
}

// Client is the mobile core. It is read-only for existing objects and drives
// append-only-unique uploads; it never modifies an existing path.
type Client struct {
	Transport Transport
	Store     Store
}

// Refresh fetches the manifest for repoID using the two-dimension known state,
// reconciles it into the local cache (monotonic, never rolled back), and returns
// the current manifest. A NOT_MODIFIED response keeps the cached manifest.
func (c Client) Refresh(ctx context.Context, repoID string) (*v1.Manifest, error) {
	cached, err := c.Store.LoadManifest(repoID)
	if err != nil {
		return nil, err
	}
	var knownGen, knownRev int64
	if cached != nil {
		knownGen, knownRev = cached.ViewGeneration, cached.RepoRevision
	}
	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{
		RepoID: repoID, KnownViewGeneration: knownGen, KnownRepoRevision: knownRev,
	})
	if err != nil {
		return nil, err
	}
	resp, _, err := c.Transport.Do(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status != v1.StatusOK {
		return nil, respError(resp)
	}
	var res v1.RefreshManifestResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode refresh result: %w", err)
	}
	if res.NotModified {
		return cached, nil
	}
	if res.Manifest == nil {
		return nil, errors.New("refresh returned neither manifest nor not_modified")
	}
	if _, err := c.Store.SaveManifestIfNewer(res.Manifest); err != nil {
		return nil, err
	}
	return res.Manifest, nil
}

// Read fetches one existing object. Append-only does not mean the phone
// cannot download; it only forbids modifying or deleting the path.
func (c Client) Read(ctx context.Context, repoID, path string) ([]byte, error) {
	req, err := v1.NewRequest(uuid.NewString(), v1.OpReadObject, v1.ReadObjectPayload{RepoID: repoID, Path: path})
	if err != nil {
		return nil, err
	}
	resp, payload, err := c.Transport.Do(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status != v1.StatusOK {
		return nil, respError(resp)
	}
	return payload, nil
}

// ListRepositories returns the authenticated installation's realm
// projection. Mobile never creates repositories: later operations must
// send a repo_id from this list.
func (c Client) ListRepositories(ctx context.Context) (*v1.ListRepositoriesResult, error) {
	req, err := v1.NewRequest(uuid.NewString(), v1.OpListRepositories, v1.ListRepositoriesPayload{})
	if err != nil {
		return nil, err
	}
	resp, _, err := c.Transport.Do(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status != v1.StatusOK {
		return nil, respError(resp)
	}
	var res v1.ListRepositoriesResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode list repositories result: %w", err)
	}
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return &res, nil
}

// DrainPending sends every non-terminal queued upload for repoID, one at a
// time, oldest first, and records whatever the worker decides. It never
// renames or auto-resolves a collision: NAME_TAKEN_DIFF and the other
// non-committed outcomes are parked for the caller to resolve later (concept
// doc §6.4, §9.3, §10.2). A transport error leaves the item queued
// (pending-create) for a later drain rather than failing the whole batch, so
// one bad connection does not strand unrelated candidates.
func (c Client) DrainPending(ctx context.Context, repoID string) ([]PendingUpload, error) {
	queued, err := c.Store.ListUploads(repoID)
	if err != nil {
		return nil, err
	}
	results := make([]PendingUpload, 0, len(queued))
	for _, item := range queued {
		if item.State.terminal() {
			results = append(results, item)
			continue
		}
		item, err = c.sendOne(ctx, item)
		if err != nil {
			return results, err
		}
		results = append(results, item)
	}
	return results, nil
}

// sendOne runs one upload attempt for item and persists the outcome. A
// transport-level error (dial/handshake/frame failure) is not a domain
// outcome: item is left pending-create with LastError recorded, so the next
// DrainPending call retries it with the same request_id.
func (c Client) sendOne(ctx context.Context, item PendingUpload) (PendingUpload, error) {
	payload, err := c.Store.loadUploadPayload(item.RepoID, item.ID)
	if err != nil {
		return item, err
	}
	req, err := v1.NewRequest(item.ID, v1.OpUploadObject, v1.UploadObjectPayload{
		RepoID: item.RepoID, ParentPath: item.ParentPath, Filename: item.Filename,
		Size: item.Size, Sha256: item.Sha256, ContentType: item.ContentType,
	})
	if err != nil {
		return item, err
	}

	item.State = UploadUploading
	if err := c.Store.recordUploadOutcome(item); err != nil {
		return item, err
	}

	resp, _, err := c.Transport.Do(ctx, req, payload)
	if err != nil {
		item.State, item.LastError = UploadPendingCreate, err.Error()
		if recErr := c.Store.recordUploadOutcome(item); recErr != nil {
			return item, recErr
		}
		return item, nil
	}
	if resp.Status != v1.StatusOK {
		return item, respError(resp)
	}
	var result v1.UploadObjectResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return item, fmt.Errorf("decode upload result: %w", err)
	}

	item.Outcome, item.LastError = result.Outcome, ""
	switch result.Outcome {
	case v1.OutcomeCommitted:
		item.State, item.Revision, item.FinalPath = UploadCommitted, result.Revision, result.FinalPath
	case v1.OutcomeNameTakenSame:
		item.State, item.ExistingSha256 = UploadDroppedSame, result.ExistingSha256
	case v1.OutcomeNameTakenDiff:
		item.State, item.ExistingSha256 = UploadConflict, result.ExistingSha256
	default:
		item.State = UploadParked
	}
	if err := c.Store.recordUploadOutcome(item); err != nil {
		return item, err
	}
	return item, nil
}

func respError(resp v1.Response) error {
	if resp.Error != nil {
		return fmt.Errorf("mobile operation failed: %s", resp.Error.Code)
	}
	return errors.New("mobile operation failed")
}
