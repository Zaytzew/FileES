package androidbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"filees/pkg/mobileclient"
	"filees/pkg/mobileclient/sshtransport"

	"golang.org/x/crypto/ssh"
)

const (
	refreshTimeout  = 30 * time.Second
	drainTimeout    = 2 * time.Minute
	downloadTimeout = 2 * time.Minute
)

// Client is the gomobile-bindable handle onto the whole mobile core: a
// persistent device identity, the embedded-SSH transport and the local store,
// wired together behind plain string/[]byte/error — Kotlin never sees the v1
// or ssh types directly, for the same reason as Store in androidbind.go.
type Client struct {
	inner mobileclient.Client
	ident identity
}

// NewClient loads or creates the device's persistent Ed25519 identity under
// storeDir (see identity.go — generated once, never leaves the device),
// wires an embedded-SSH transport pinned to hostPublicKey, and returns a
// ready client. storeDir must be the app's non-evictable filesDir; address is
// "host:port"; hostPublicKey is one bare "ssh-ed25519 AAAA..." line pinned
// during onboarding.
func NewClient(storeDir, address, user, hostPublicKey string) (*Client, error) {
	if strings.TrimSpace(storeDir) == "" {
		return nil, errors.New("androidbind: store_dir is required")
	}
	ident, err := loadOrCreateIdentity(storeDir)
	if err != nil {
		return nil, fmt.Errorf("androidbind: identity: %w", err)
	}
	transport, err := sshtransport.New(sshtransport.Config{
		Address:       address,
		User:          user,
		HostPublicKey: hostPublicKey,
		Signer:        ident.signer,
	})
	if err != nil {
		return nil, err
	}
	return &Client{
		inner: mobileclient.Client{Transport: transport, Store: mobileclient.Store{Root: storeDir}},
		ident: ident,
	}, nil
}

// PublicKey returns this device's own SSH public key in authorized_keys
// format, to be shown to the user and pinned/approved server-side during
// onboarding. The matching private key never leaves the device.
func (c *Client) PublicKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(c.ident.signer.PublicKey())))
}

// ListRepositoriesJSON returns the installation's realm projection as JSON
// (view_generation, realm_alias, repositories[{repo_id, display_name,
// access, state}]). Mobile never creates repositories: the UI picks one
// of these shares and later operations send that repo_id.
func (c *Client) ListRepositoriesJSON() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	res, err := c.inner.ListRepositories(ctx)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// RefreshJSON fetches (or confirms unchanged) the manifest for repoID and
// returns it as JSON, or "" if nothing has ever been cached and the server
// reports no manifest either.
func (c *Client) RefreshJSON(repoID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	manifest, err := c.inner.Refresh(ctx, repoID)
	if err != nil {
		return "", err
	}
	if manifest == nil {
		return "", nil
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DownloadTo writes the object at path into destPath. The phone is allowed
// to read existing objects; it still cannot modify or delete them.
func (c *Client) DownloadTo(repoID, path, destPath string) error {
	if strings.TrimSpace(destPath) == "" {
		return errors.New("androidbind: dest_path is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	data, err := c.inner.Read(ctx, repoID, path)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o600)
}

// EnqueueUpload durably queues a new append-only-unique candidate (concept
// doc §9.2 — never a candidate for re-upload of the same path) and returns
// its id, which doubles as the wire request_id for every drain attempt.
func (c *Client) EnqueueUpload(repoID, parentPath, filename, contentType string, content []byte) (string, error) {
	item, err := c.inner.Store.EnqueueUpload(repoID, parentPath, filename, contentType, content)
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

// DrainPendingJSON sends every non-terminal queued upload for repoID and
// returns the resulting items (including already-terminal ones) as a JSON
// array — see mobileclient.PendingUpload for the shape.
func (c *Client) DrainPendingJSON(repoID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	items, err := c.inner.DrainPending(ctx, repoID)
	if err != nil {
		return "", err
	}
	if items == nil {
		items = []mobileclient.PendingUpload{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ListUploadsJSON returns every queued candidate for repoID, in any state
// (including conflict/parked ones awaiting a decision), as a JSON array,
// oldest first.
func (c *Client) ListUploadsJSON(repoID string) (string, error) {
	items, err := c.inner.Store.ListUploads(repoID)
	if err != nil {
		return "", err
	}
	if items == nil {
		items = []mobileclient.PendingUpload{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DiscardUpload removes a queued candidate outright — the explicit "reject"
// half of the conflict/parked decision (concept doc §6.4: "albo odrzuca").
func (c *Client) DiscardUpload(repoID, id string) error {
	return c.inner.Store.DiscardUpload(repoID, id)
}
