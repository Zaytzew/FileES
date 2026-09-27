package androidbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	v1 "filees/pkg/mobile/v1"
	"filees/pkg/mobileclient"
	"filees/pkg/mobileclient/sshtransport"

	"golang.org/x/crypto/ssh"
)

const (
	refreshTimeout  = 30 * time.Second
	downloadTimeout = 2 * time.Minute
)

// Client is the gomobile-bindable handle onto the whole mobile core: a
// persistent device identity, the embedded-SSH transport and the local store,
// wired together behind plain string/[]byte/error — Kotlin never sees the v1
// or ssh types directly, for the same reason as Store in androidbind.go.
type Client struct {
	inner  mobileclient.Client
	ident  identity
	ctx    context.Context
	cancel context.CancelFunc
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{ctx: ctx, cancel: cancel,
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

// PublicKeyIn reads the device key already stored under storeDir. It does not
// open a connection, so Settings can show the key even when the server
// address or host key will not build a client.
func PublicKeyIn(storeDir string) (string, error) {
	ident, err := loadOrCreateIdentity(storeDir)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ident.signer.PublicKey()))), nil
}

// ListRepositoriesJSON returns the installation's realm projection as JSON
// (view_generation, realm_alias, server_display_name, generated_at,
// repositories[{repo_id, display_name, access, state, purpose}]). Mobile never creates repositories:
// the UI picks one of these shares and later operations send that repo_id.
func (c *Client) ListRepositoriesJSON() (string, error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), refreshTimeout)
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

// ListDrawersJSON returns the realm drawer frame as JSON
// ({version, drawers[{id,name}], assignments{repo_id:drawer_id}}).
// An error means the phone should keep the repository list flat.
func (c *Client) ListDrawersJSON() (string, error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), refreshTimeout)
	defer cancel()
	res, err := c.inner.ListDrawers(ctx)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// RequestDesktopJoin asks the server to mail a normal desktop invitation to
// email. The phone never receives the invite blob; mail + BeginInvitation
// stay the desktop path.
func (c *Client) RequestDesktopJoin(email string) error {
	ctx, cancel := context.WithTimeout(c.baseContext(), refreshTimeout)
	defer cancel()
	return c.inner.RequestDesktopJoin(ctx, email)
}

// RefreshJSON fetches (or confirms unchanged) the manifest for repoID and
// returns it as JSON, or "" if nothing has ever been cached and the server
// reports no manifest either.
func (c *Client) RefreshJSON(repoID string) (string, error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), refreshTimeout)
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

// ListDirectoryJSON returns immediate children of path as a manifest JSON
// (entries in the object, not a recursive tree). revision/generation pin the
// local directory cache; 0 lets the worker use HEAD.
func (c *Client) ListDirectoryJSON(repoID, path string, revision, generation int64) (string, error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), refreshTimeout)
	defer cancel()
	m, err := c.inner.ListDirectory(ctx, repoID, path, generation, revision)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ListFilesUnderJSON walks directory pages and returns {"entries":[files…]}.
func (c *Client) ListFilesUnderJSON(repoID, path string, revision, generation int64) (string, error) {
	ctx, cancel := context.WithTimeout(c.baseContext(), downloadTimeout)
	defer cancel()
	files, err := c.inner.ListFilesUnder(ctx, repoID, path, generation, revision)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"entries": files})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// DownloadTo writes the object at path into destPath. The phone is allowed
// to read existing objects; it still cannot modify or delete them.
func (c *Client) DownloadTo(repoID, path, destPath string) error {
	if strings.TrimSpace(destPath) == "" {
		return errors.New("androidbind: dest_path is required")
	}
	ctx, cancel := context.WithTimeout(c.baseContext(), downloadTimeout)
	defer cancel()
	data, err := c.inner.Read(ctx, repoID, path)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, data, 0o600)
}

// UploadTreeFile sends a zip produced by the Android packer as one UPLOAD_TREE
// frame. The worker unpacks a filees.tree/v1 pack under mobile-uploads/.
func (c *Client) UploadTreeFile(repoID, parentPath string, fileCount int, zipPath string) error {
	id, err := c.EnqueueTreeFile(repoID, parentPath, fileCount, zipPath, "[]")
	if err != nil {
		return err
	}
	item, err := c.inner.SendUpload(c.baseContext(), repoID, id)
	if err != nil {
		return err
	}
	if item.State != mobileclient.UploadCommitted {
		return fmt.Errorf("UPLOAD_TREE: %s: %s", item.State, item.LastError)
	}
	return nil
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
	// No batch deadline: each queued file has its own timeout inside DrainPending.
	items, err := c.inner.DrainPending(c.baseContext(), repoID)
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

// RetryUploadAs requeues a conflict/parked candidate under filename
// (empty keeps the original name) with a new request_id.
func (c *Client) RetryUploadAs(repoID, id, filename string) error {
	_, err := c.inner.Store.RetryUploadAsContext(c.baseContext(), repoID, id, filename)
	return err
}

// Cancel closes pending network I/O on this handle. A stopped worker creates
// a fresh handle on its next run; cancellation cannot cancel another worker.
func (c *Client) Cancel() {
	if c.cancel != nil {
		c.cancel()
	}
}
func (c *Client) baseContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *Client) enqueueFile(repoID, parent, name, contentType, filePath, sourcesJSON string, operation v1.Operation, count int) (string, error) {
	var sources []string
	if err := json.Unmarshal([]byte(sourcesJSON), &sources); err != nil {
		return "", err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	item, err := c.inner.Store.EnqueueReader(c.baseContext(), repoID, parent, name, contentType, operation, count, sources, f)
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

// EnqueueTreeFile durably imports the streamed ZIP, keeping its sources and ID.
func (c *Client) EnqueueTreeFile(repoID, parent string, count int, filePath, sourcesJSON string) (string, error) {
	return c.enqueueFile(repoID, parent, "tree.zip", "application/zip", filePath, sourcesJSON, v1.OpUploadTree, count)
}

// EnqueueUploadFile streams a SAF spool into the durable queue without []byte.
func (c *Client) EnqueueUploadFile(repoID, parent, name, contentType, filePath, sourcesJSON string) (string, error) {
	return c.enqueueFile(repoID, parent, name, contentType, filePath, sourcesJSON, v1.OpUploadObject, 1)
}

// SendUploadJSON reports only the requested ID, including uncertain/parked.
func (c *Client) SendUploadJSON(repoID, id string) (string, error) {
	item, err := c.inner.SendUpload(c.baseContext(), repoID, id)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal([]mobileclient.PendingUpload{item})
	return string(raw), err
}
