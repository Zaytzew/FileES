package mobileclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
)

// sendOneTimeout is the base processing budget; transfer size adds time. A folder of
// many files must not share one deadline: the previous 2-minute batch
// timer cancelled a later dial mid-lookup and looked like a DNS failure
// even though earlier files had already landed.
const sendOneTimeout = 2 * time.Minute

// Transport carries one framed mobile operation to the server and returns the
// response. Implementations wrap an SSH session (one operation per session); the
// core stays transport-neutral so it can be tested against an in-process worker.
type Transport interface {
	Do(ctx context.Context, req v1.Request, reqPayload []byte) (resp v1.Response, respPayload []byte, err error)
}

// Client reads repository views and drives durable capture uploads. Object
// uploads append unique names; tree uploads may replace within mobile-uploads.
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

const maxListedFiles = 200

// ListDirectory returns immediate children of path at revision, using the
// local directory cache when generation and revision match.
func (c Client) ListDirectory(ctx context.Context, repoID, path string, generation, revision int64) (*v1.Manifest, error) {
	path = strings.Trim(path, "/")
	if cached, err := c.Store.LoadDirectory(repoID, path, generation, revision); err != nil {
		return nil, err
	} else if cached != nil {
		return cached, nil
	}
	req, err := v1.NewRequest(uuid.NewString(), v1.OpListDirectory, v1.ListDirectoryPayload{
		RepoID: repoID, Path: path, Revision: revision,
	})
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
	var m v1.Manifest
	if err := json.Unmarshal(resp.Result, &m); err != nil {
		return nil, fmt.Errorf("decode list directory result: %w", err)
	}
	if len(payload) > 0 && string(payload) != "null" {
		if err := json.Unmarshal(payload, &m.Entries); err != nil {
			return nil, fmt.Errorf("decode list directory entries: %w", err)
		}
	}
	if m.Entries == nil {
		m.Entries = []v1.ManifestEntry{}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := c.Store.SaveDirectory(path, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListFilesUnder walks LIST_DIRECTORY pages and returns files under path,
// stopping after maxListedFiles. Used for folder download, not browse.
func (c Client) ListFilesUnder(ctx context.Context, repoID, path string, generation, revision int64) ([]v1.ManifestEntry, error) {
	var files []v1.ManifestEntry
	var walk func(string) error
	walk = func(dir string) error {
		m, err := c.ListDirectory(ctx, repoID, dir, generation, revision)
		if err != nil {
			return err
		}
		for _, e := range m.Entries {
			if e.Kind == v1.KindFile {
				files = append(files, e)
				if len(files) > maxListedFiles {
					return fmt.Errorf("mobile operation failed: manifest.too_large: folder has more than %d files", maxListedFiles)
				}
			}
		}
		for _, e := range m.Entries {
			if e.Kind == v1.KindDirectory {
				if err := walk(e.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(strings.Trim(path, "/")); err != nil {
		return nil, err
	}
	return files, nil
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

// ListDrawers asks for the realm's drawer frame. A missing command, an empty
// document and a realm with no drawers are all normal: the caller keeps the
// repository list flat. This method returns the transport error unchanged so
// the phone can make that choice.
func (c Client) ListDrawers(ctx context.Context) (*v1.ListDrawersResult, error) {
	req, err := v1.NewRequest(uuid.NewString(), v1.OpListDrawers, v1.ListDrawersPayload{})
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
	var res v1.ListDrawersResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode list drawers result: %w", err)
	}
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return &res, nil
}

// RequestDesktopJoin asks the server to issue a normal join invitation to
// email. The response must not contain the invite; mail delivers it.
func (c Client) RequestDesktopJoin(ctx context.Context, email string) error {
	req, err := v1.NewRequest(uuid.NewString(), v1.OpRequestDesktopJoin, v1.RequestDesktopJoinPayload{Email: email})
	if err != nil {
		return err
	}
	resp, _, err := c.Transport.Do(ctx, req, nil)
	if err != nil {
		return err
	}
	if resp.Status != v1.StatusOK {
		return respError(resp)
	}
	return nil
}

// UploadTree is the byte-slice compatibility entrypoint. The durable queue
// owns its ID and payload even if both the upload ACK and status are lost.
func (c Client) UploadTree(ctx context.Context, repoID, parentPath string, fileCount int, zip []byte) error {
	item, err := c.Store.EnqueueReader(ctx, repoID, parentPath, "tree.zip", "application/zip", v1.OpUploadTree, fileCount, nil, bytes.NewReader(zip))
	if err != nil {
		return err
	}
	item, err = c.SendUpload(ctx, repoID, item.ID)
	if err != nil {
		return err
	}
	if item.State != UploadCommitted {
		return fmt.Errorf("UPLOAD_TREE: %s: %s", item.State, item.LastError)
	}
	return nil
}

func decodeTreeReceipt(resp v1.Response, item PendingUpload, receipt *v1.UploadTreeResult) error {
	if resp.RequestID != item.ID || resp.Operation != v1.OpUploadTree {
		return errors.New("UPLOAD_TREE: unrelated receipt")
	}
	if err := json.Unmarshal(resp.Result, receipt); err != nil {
		return err
	}
	if receipt.FileCount != item.FileCount || receipt.Size != item.Size || receipt.Revision < 1 {
		return errors.New("UPLOAD_TREE: incomplete receipt")
	}
	return nil
}

// operationStatus asks the server what it durably recorded for requestID
// (GET_OPERATION_STATUS backed by the same ledger UploadTree's idempotent
// retry check reads). Any failure here - including one from the same flaky
// link - returns OpStateUnknown: the caller must not read "the status check
// itself failed" as "the operation failed", only as "still don't know".
func (c Client) operationReceipt(ctx context.Context, requestID string) (v1.OperationStatusResult, error) {
	req, err := v1.NewRequest(uuid.NewString(), v1.OpOperationStatus, v1.OperationStatusPayload{TargetRequestID: requestID})
	if err != nil {
		return v1.OperationStatusResult{}, err
	}
	resp, _, err := c.Transport.Do(ctx, req, nil)
	if err != nil {
		return v1.OperationStatusResult{}, err
	}
	if resp.RequestID != req.RequestID || resp.Operation != req.Operation {
		return v1.OperationStatusResult{}, errors.New("unrelated status receipt")
	}
	if resp.Status != v1.StatusOK {
		return v1.OperationStatusResult{}, respError(resp)
	}
	var result v1.OperationStatusResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return v1.OperationStatusResult{}, err
	}
	return result, nil
}

// DrainPending sends every non-terminal queued upload for repoID, one at a
// time, oldest first, and records whatever the worker decides. It never
// renames or auto-resolves a collision: NAME_TAKEN_DIFF and the other
// non-committed outcomes are parked for the caller to resolve later (concept
// doc §6.4, §9.3, §10.2). A transport error leaves the item queued
// (pending-create) for a later drain rather than failing the whole batch, so
// one bad connection does not strand unrelated candidates.
func (c Client) DrainPending(ctx context.Context, repoID string) ([]PendingUpload, error) {
	unlock, err := c.Store.lockQueue(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	queued, err := c.Store.ListUploads(repoID)
	if err != nil {
		return nil, err
	}
	// Unattempted work runs first. A huge/slow item cancelled by Android's
	// worker budget must not remain the first item on every next tick.
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].LastAttemptAt.Before(queued[j].LastAttemptAt) })

	results := make([]PendingUpload, 0, len(queued))
	for _, item := range queued {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if item.State.terminal() {
			results = append(results, item)
			continue
		}
		itemCtx, cancel := context.WithTimeout(ctx, uploadAttemptTimeout(item))
		item, err = c.sendOne(itemCtx, ctx, item)
		cancel()
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
func (c Client) sendOne(ctx, caller context.Context, item PendingUpload) (PendingUpload, error) {
	if !item.LastAttemptAt.IsZero() || item.State == UploadUploading {
		if resolved, ok := c.recoverReceipt(caller, item); ok {
			return resolved, c.Store.recordUploadOutcome(resolved)
		}
	}

	if item.Operation == v1.OpUploadTree {
		return c.sendTree(ctx, caller, item)
	}
	req, err := v1.NewRequest(item.ID, v1.OpUploadObject, v1.UploadObjectPayload{
		RepoID: item.RepoID, ParentPath: item.ParentPath, Filename: item.Filename,
		Size: item.Size, Sha256: item.Sha256, ContentType: item.ContentType,
	})
	if err != nil {
		item.State, item.LastError = UploadParked, err.Error()
		return item, c.Store.recordUploadOutcome(item)
	}

	item.State, item.LastAttemptAt = UploadUploading, time.Now().UTC()
	if err := c.Store.recordUploadOutcome(item); err != nil {
		return item, err
	}

	resp, _, err := c.sendPayload(ctx, req, item)
	if err != nil {
		if resolved, ok := c.recoverReceipt(caller, item); ok {
			return resolved, c.Store.recordUploadOutcome(resolved)
		}
		item.State, item.LastError = UploadPendingCreate, err.Error()
		if recErr := c.Store.recordUploadOutcome(item); recErr != nil {
			return item, recErr
		}
		return item, nil
	}
	if resp.Status != v1.StatusOK {
		return c.pendingError(item, respError(resp))
	}
	var result v1.UploadObjectResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return c.pendingError(item, fmt.Errorf("decode upload result: %w", err))
	}

	if resp.RequestID != item.ID || resp.Operation != v1.OpUploadObject ||
		(result.Outcome == v1.OutcomeCommitted && (result.Revision < 1 || result.FinalPath != strings.Trim(item.ParentPath, "/")+"/"+item.Filename)) ||
		(result.Outcome == v1.OutcomeNameTakenSame && result.ExistingSha256 != item.Sha256) {
		return c.pendingError(item, errors.New("UPLOAD_OBJECT: incomplete receipt"))
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
		if strings.TrimSpace(resp.Error.Message) != "" {
			return fmt.Errorf("mobile operation failed: %s: %s", resp.Error.Code, resp.Error.Message)
		}
		return fmt.Errorf("mobile operation failed: %s", resp.Error.Code)
	}
	return errors.New("mobile operation failed")
}
