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

func respError(resp v1.Response) error {
	if resp.Error != nil {
		return fmt.Errorf("mobile operation failed: %s", resp.Error.Code)
	}
	return errors.New("mobile operation failed")
}
