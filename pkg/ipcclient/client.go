// Package ipcclient is the shared FileES IPC client used by CLI and GUI.
// It speaks filees.contract/v1 over a Unix domain socket.
// Import this package instead of any engine package (commit, watcher, client).
package ipcclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	contract "filees/pkg/contract/v1"
	"filees/pkg/realmbranding"
)

const defaultTimeout = 10 * time.Second

// ResponseError preserves the structured error returned by the daemon.  It is
// safe to inspect with errors.As; callers must treat Details as diagnostic data
// and must not parse it to drive application behaviour.
type ResponseError struct {
	Body contract.ErrorBody
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Body.Code, e.Body.MessageKey)
}

// PresentationError exposes the presentation-safe portion without requiring
// GUI layers to import ipcclient or the wire-contract package.
func (e *ResponseError) PresentationError() (code, severity, hint, message string) {
	return e.Body.Code, e.Body.Severity, e.Body.Hint, e.Body.MessageKey
}

// PresentationDetails exposes the structured fields that belong to a message
// key, so a caller that recognises the key can name the thing that went wrong
// instead of printing a generic sentence.
//
// This deliberately does not make every detail presentable. Details attached
// to unknown keys are frequently raw command output; the contract is that a
// caller reads only the fields its own key defines, which is why this returns
// a copy rather than being folded into PresentationError.
func (e *ResponseError) PresentationDetails() map[string]string {
	if len(e.Body.Details) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.Body.Details))
	for key, value := range e.Body.Details {
		out[key] = value
	}
	return out
}

// Client sends contract requests to the FileES daemon over a Unix socket.
// A new TCP-style connection is used for each request; safe for concurrent use.
type Client struct {
	sockPath string
	timeout  time.Duration
	clientID string
}

// New creates a Client. clientID is embedded in every request envelope.
func New(sockPath, clientID string) *Client {
	return &Client{sockPath: sockPath, timeout: defaultTimeout, clientID: clientID}
}

// DefaultSocketPath returns the canonical per-user socket path — mirrors
// the path chosen by ipcserver.DefaultSocketPath.
func DefaultSocketPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "filees.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".filees", "daemon.sock")
}

// Do sends req to the daemon and returns the response.
// A response with Status=="error" is returned without a Go error —
// call resp.Error for details. Use the typed helpers below where possible.
func (c *Client) Do(ctx context.Context, req contract.Request) (contract.Response, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return contract.Response{}, fmt.Errorf("daemon unreachable (%s): %w", c.sockPath, err)
	}
	defer conn.Close()

	// Use exactly the caller's context deadline if present; otherwise fall back to c.timeout.
	// This makes short-deadline callers fail fast and long-deadline callers (e.g. lock/unlock) wait longer.
	dl, hasDL := ctx.Deadline()
	if !hasDL {
		dl = time.Now().Add(c.timeout)
	}
	_ = conn.SetDeadline(dl)

	// Propagate context cancellation: close the connection so blocked I/O unblocks immediately.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return contract.Response{}, fmt.Errorf("send: %w", err)
	}

	var resp contract.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return contract.Response{}, fmt.Errorf("receive: %w", err)
	}
	if resp.Protocol != contract.Protocol {
		return contract.Response{}, fmt.Errorf("protocol mismatch: got %q want %q", resp.Protocol, contract.Protocol)
	}
	if resp.RequestID != req.RequestID {
		return contract.Response{}, fmt.Errorf("request_id mismatch: sent %s got %s", req.RequestID, resp.RequestID)
	}
	if resp.Status != contract.StatusOK && resp.Status != contract.StatusError {
		return contract.Response{}, fmt.Errorf("unknown status %q", resp.Status)
	}
	return resp, nil
}

// --- typed helpers ---

func (c *Client) Hello(ctx context.Context) (*contract.HelloResult, error) {
	resp, err := c.do(ctx, contract.CmdSystemHello, "", nil)
	if err != nil {
		return nil, err
	}
	var r contract.HelloResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) SystemStatus(ctx context.Context) (*contract.SystemStatusResult, error) {
	resp, err := c.do(ctx, contract.CmdSystemStatus, "", nil)
	if err != nil {
		return nil, err
	}
	var r contract.SystemStatusResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) SystemRestart(ctx context.Context) (*contract.SystemLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdSystemRestart, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.SystemLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) SystemShutdown(ctx context.Context) (*contract.SystemLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdSystemShutdown, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.SystemLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UpdateStatus(ctx context.Context) (*contract.UpdateStatus, error) {
	resp, err := c.do(ctx, contract.CmdUpdateStatus, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.UpdateStatus
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UpdatePlan(ctx context.Context) (*contract.UpdatePlanResult, error) {
	resp, err := c.do(ctx, contract.CmdUpdatePlan, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.UpdatePlanResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UpdateApply(ctx context.Context) (*contract.UpdateApplyResult, error) {
	resp, err := c.do(ctx, contract.CmdUpdateApply, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.UpdateApplyResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) ActivationBegin(ctx context.Context, payload contract.ActivationBeginPayload) (*contract.ActivationCommandResult, error) {
	resp, err := c.do(ctx, contract.CmdActivationBegin, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.ActivationCommandResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) ActivationFinish(ctx context.Context, payload contract.ActivationFinishPayload) (*contract.ActivationCommandResult, error) {
	resp, err := c.do(ctx, contract.CmdActivationFinish, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.ActivationCommandResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) ActivationPending(ctx context.Context, payload contract.ActivationPendingPayload) (*contract.ActivationPendingResult, error) {
	resp, err := c.do(ctx, contract.CmdActivationPending, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.ActivationPendingResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) ActivationResume(ctx context.Context, payload contract.ActivationResumePayload) (*contract.ActivationCommandResult, error) {
	resp, err := c.do(ctx, contract.CmdActivationResume, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.ActivationCommandResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoList(ctx context.Context) (*contract.RepoListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoList, "", nil)
	if err != nil {
		return nil, err
	}
	var r contract.RepoListResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) RepoActivity(ctx context.Context, limit int) (*contract.RepoActivityResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoActivity, "", contract.RepoActivityPayload{Limit: limit})
	if err != nil {
		return nil, err
	}
	var result contract.RepoActivityResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoCreateRequest(ctx context.Context, payload contract.RepoCreateRequestPayload) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoCreateRequest, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoAttachIntent(ctx context.Context, payload contract.RepoAttachIntentPayload) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAttachIntent, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoAttachApprove(ctx context.Context, payload contract.RepoAttachApprovePayload) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoAttachApprove, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoLocate(ctx context.Context, payload contract.RepoLocatePayload) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoLocate, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) ServerDetach(ctx context.Context, serverID string) (*contract.ServerDetachResult, error) {
	resp, err := c.do(ctx, contract.CmdServerDetach, "", contract.ServerDetachPayload{ServerID: serverID})
	if err != nil {
		return nil, err
	}
	var result contract.ServerDetachResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RealmRemoveBegin(ctx context.Context, payload contract.RealmRemoveBeginPayload) (*contract.RealmRemoveBeginResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmRemoveBegin, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RealmRemoveBeginResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RealmRemoveConfirm(ctx context.Context, payload contract.RealmRemoveConfirmPayload) (*contract.RealmRemoveConfirmResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmRemoveConfirm, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RealmRemoveConfirmResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RecoveryDownload(ctx context.Context, payload contract.RecoveryDownloadPayload) (*contract.RecoveryDownloadResult, error) {
	resp, err := c.do(ctx, contract.CmdRecoveryDownload, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.RecoveryDownloadResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) MobilePairingBegin(ctx context.Context, serverID string) (*contract.MobilePairingBeginResult, error) {
	resp, err := c.do(ctx, contract.CmdMobilePairingBegin, "", contract.MobilePairingBeginPayload{ServerID: serverID})
	if err != nil {
		return nil, err
	}
	var result contract.MobilePairingBeginResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoLifecycleStatus(ctx context.Context, operationID string) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoLifecycleStatus, "", contract.RepoLifecycleStatusPayload{OperationID: operationID})
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoDetach(ctx context.Context, serverID, repoID string) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoDetach, repoID, contract.RepoDetachPayload{ServerID: serverID, RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoLoadDump(ctx context.Context, serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoLoadDump, repoID, contract.RepoLoadDumpPayload{ServerID: serverID, RepoID: repoID, ApplyCurrentIgnorePolicy: applyIgnorePolicy, KeepLastRevisions: keepLastRevisions})
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RealmGrantRecipients(ctx context.Context, serverID, repoID string) (*contract.RealmGrantRecipientsResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmGrantRecipients, repoID, contract.RealmGrantRecipientsPayload{ServerID: serverID, RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var result contract.RealmGrantRecipientsResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RealmSetVisibility(ctx context.Context, serverID, visibility string) (*contract.RealmSetVisibilityResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmSetVisibility, "", contract.RealmSetVisibilityPayload{ServerID: serverID, Visibility: visibility})
	if err != nil {
		return nil, err
	}
	var result contract.RealmSetVisibilityResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RealmPublicBranding(ctx context.Context, serverID string) (*contract.RealmPublicBrandingResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmPublicBrandingGet, "", contract.RealmPublicBrandingGetPayload{ServerID: serverID})
	if err != nil {
		return nil, err
	}
	var result contract.RealmPublicBrandingResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RealmSetPublicBranding(ctx context.Context, serverID string, branding realmbranding.Branding) (*contract.RealmPublicBrandingResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmPublicBrandingSet, "", contract.RealmPublicBrandingSetPayload{ServerID: serverID, Branding: branding})
	if err != nil {
		return nil, err
	}
	var result contract.RealmPublicBrandingResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RepoGrantAccess(ctx context.Context, payload contract.RepoGrantAccessPayload) (*contract.RealmGrantResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoGrantAccess, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RealmGrantResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ServerSetSessionTimeout(ctx context.Context, payload contract.ServerSetSessionTimeoutPayload) (*contract.ServerSetSessionTimeoutResult, error) {
	resp, err := c.do(ctx, contract.CmdServerSetSessionTimeout, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.ServerSetSessionTimeoutResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RepoSetEditingPolicy(ctx context.Context, payload contract.RepoSetEditingPolicyPayload) (*contract.RepoSetEditingPolicyResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoSetEditingPolicy, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RepoSetEditingPolicyResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RepoRevokeAccess(ctx context.Context, payload contract.RepoRevokeAccessPayload) (*contract.RealmGrantResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoRevokeAccess, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.RealmGrantResult
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) RepoDelete(ctx context.Context, serverID, repoID string) (*contract.RepoLifecycleResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoDelete, repoID, contract.RepoDetachPayload{ServerID: serverID, RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var result contract.RepoLifecycleResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) PublicShareList(ctx context.Context, serverID, repoID string) (*contract.PublicShareListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareList, repoID, contract.PublicShareListPayload{ServerID: serverID, RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

// PublicShareListAll returns the daemon's cached, cross-repo aggregate of
// every owned public share across every activated server.
func (c *Client) PublicShareListAll(ctx context.Context) (*contract.PublicShareListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareListAll, "", nil)
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) PublicShareCreate(ctx context.Context, payload contract.PublicShareCreatePayload) (*contract.PublicShareResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareCreate, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) PublicShareUpdate(ctx context.Context, payload contract.PublicShareUpdatePayload) (*contract.PublicShareResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareUpdate, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) PublicShareRevoke(ctx context.Context, payload contract.PublicShareChannelPayload) (*contract.PublicShareResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareRevoke, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) PublicShareDelete(ctx context.Context, payload contract.PublicShareChannelPayload) (*contract.PublicShareResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublicShareDelete, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.PublicShareResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) QuarantineList(ctx context.Context, serverID string) (*contract.QuarantineListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoQuarantineList, "", contract.QuarantineListPayload{ServerID: serverID})
	if err != nil {
		return nil, err
	}
	var result contract.QuarantineListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) QuarantineHide(ctx context.Context, serverID, uploadID string) (*contract.QuarantineHideResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoQuarantineHide, "", contract.QuarantineItemPayload{ServerID: serverID, UploadID: uploadID})
	if err != nil {
		return nil, err
	}
	var result contract.QuarantineHideResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) QuarantineFetch(ctx context.Context, serverID, uploadID string) (*contract.QuarantineFetchResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoQuarantineFetch, "", contract.QuarantineItemPayload{ServerID: serverID, UploadID: uploadID})
	if err != nil {
		return nil, err
	}
	var result contract.QuarantineFetchResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UploadChannelList(ctx context.Context, serverID, repoID string) (*contract.UploadChannelListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoUploadChannelList, repoID, contract.UploadChannelListPayload{ServerID: serverID, RepoID: repoID})
	if err != nil {
		return nil, err
	}
	var result contract.UploadChannelListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UploadChannelCreate(ctx context.Context, payload contract.UploadChannelCreatePayload) (*contract.UploadChannelResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoUploadChannelCreate, payload.AuthorityRepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.UploadChannelResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UploadChannelUpdate(ctx context.Context, payload contract.UploadChannelUpdatePayload) (*contract.UploadChannelResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoUploadChannelUpdate, payload.AuthorityRepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.UploadChannelResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UploadChannelRevoke(ctx context.Context, payload contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoUploadChannelRevoke, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.UploadChannelResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) UploadChannelDelete(ctx context.Context, payload contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoUploadChannelDelete, payload.RepoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.UploadChannelResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) RepoStatus(ctx context.Context, repoID string) (*contract.RepoStatus, error) {
	resp, err := c.do(ctx, contract.CmdRepoStatus, repoID, nil)
	if err != nil {
		return nil, err
	}
	var r contract.RepoStatus
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) ErrorList(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error) {
	resp, err := c.do(ctx, contract.CmdErrorList, pl.RepoID, pl)
	if err != nil {
		return nil, err
	}
	var r contract.ErrorListResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) Lock(ctx context.Context, repoID string, paths []string) (string, error) {
	resp, err := c.do(ctx, contract.CmdRepoLock, repoID, contract.RepoLockPayload{Paths: paths})
	if err != nil {
		return "", err
	}
	var r contract.LockResult
	return r.Output, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) Unlock(ctx context.Context, repoID string, paths []string) (string, error) {
	resp, err := c.do(ctx, contract.CmdRepoUnlock, repoID, contract.RepoLockPayload{Paths: paths})
	if err != nil {
		return "", err
	}
	var r contract.LockResult
	return r.Output, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) RepoPublish(ctx context.Context, repoID, comment string) (*contract.RepoPublishResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoPublish, repoID, contract.RepoPublishPayload{Comment: comment})
	if err != nil {
		return nil, err
	}
	var result contract.RepoPublishResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) NoticeList(ctx context.Context) (*contract.NoticeListResult, error) {
	resp, err := c.do(ctx, contract.CmdNoticeList, "", struct{}{})
	if err != nil {
		return nil, err
	}
	var result contract.NoticeListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) NoticeAck(ctx context.Context, noticeID string) error {
	resp, err := c.do(ctx, contract.CmdNoticeAck, "", contract.NoticeAckPayload{NoticeID: noticeID})
	if err != nil {
		return err
	}
	var result map[string]bool
	return contract.DecodeResult(resp.Result, &result)
}

// RepoReservationList returns the live SVN lock inventory visible in this
// installation's attached working copies for one activated server.
func (c *Client) RepoReservationList(ctx context.Context, serverID string) (*contract.RepoReservationListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoReservationList, "", contract.RepoReservationListPayload{ServerID: serverID})
	if err != nil {
		return nil, err
	}
	var result contract.RepoReservationListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

// RepoReservationRelease performs a token-fenced release of one row returned
// by RepoReservationList.  It never accepts an absolute filesystem path.
func (c *Client) RepoReservationRelease(ctx context.Context, payload contract.RepoReservationReleasePayload) error {
	resp, err := c.do(ctx, contract.CmdRepoReservationRelease, payload.RepoID, payload)
	if err != nil {
		return err
	}
	var result contract.LockResult
	return contract.DecodeResult(resp.Result, &result)
}

func (c *Client) LockReleaseRequest(ctx context.Context, payload contract.LockReleaseRequestPayload) (*contract.LockReleaseRequest, error) {
	return c.lockRelease(ctx, contract.CmdLockReleaseRequest, payload.RepoID, payload)
}

func (c *Client) LockReleaseDismiss(ctx context.Context, payload contract.LockReleaseDecisionPayload) (*contract.LockReleaseRequest, error) {
	return c.lockRelease(ctx, contract.CmdLockReleaseDismiss, "", payload)
}

func (c *Client) LockReleaseAccept(ctx context.Context, payload contract.LockReleaseDecisionPayload) (*contract.LockReleaseRequest, error) {
	return c.lockRelease(ctx, contract.CmdLockReleaseAccept, "", payload)
}

func (c *Client) lockRelease(ctx context.Context, command, repoID string, payload any) (*contract.LockReleaseRequest, error) {
	resp, err := c.do(ctx, command, repoID, payload)
	if err != nil {
		return nil, err
	}
	var result contract.LockReleaseRequest
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) WhaleList(ctx context.Context) (*contract.WhaleListResult, error) {
	resp, err := c.do(ctx, contract.CmdWhaleList, "", struct{}{})
	if err != nil {
		return nil, err
	}
	var result contract.WhaleListResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

func (c *Client) WhaleGet(ctx context.Context, operationID string) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhaleGet, contract.WhaleOperationPayload{OperationID: operationID})
}

func (c *Client) WhalePutBegin(ctx context.Context, payload contract.WhalePutBeginPayload) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhalePutBegin, payload)
}

func (c *Client) WhaleGetBegin(ctx context.Context, payload contract.WhaleGetBeginPayload) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhaleGetBegin, payload)
}

func (c *Client) WhaleGetConfirm(ctx context.Context, operationID string) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhaleGetConfirm, contract.WhaleOperationPayload{OperationID: operationID})
}

func (c *Client) WhaleRetry(ctx context.Context, operationID string) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhaleRetry, contract.WhaleOperationPayload{OperationID: operationID})
}

func (c *Client) WhaleCancel(ctx context.Context, operationID string, removePayload bool) (*contract.WhaleOperation, error) {
	return c.whaleOperation(ctx, contract.CmdWhaleCancel, contract.WhaleCancelPayload{OperationID: operationID, RemovePayload: removePayload})
}

func (c *Client) whaleOperation(ctx context.Context, command string, payload any) (*contract.WhaleOperation, error) {
	resp, err := c.do(ctx, command, "", payload)
	if err != nil {
		return nil, err
	}
	var result contract.WhaleOperation
	return &result, contract.DecodeResult(resp.Result, &result)
}

// RealmAliasClaim permanently assigns the caller realm's human-facing alias.
// The daemon intentionally exposes no availability query.
func (c *Client) RealmAliasClaim(ctx context.Context, serverID, alias string) (*contract.RealmAliasClaimResult, error) {
	resp, err := c.do(ctx, contract.CmdRealmAliasClaim, "", contract.RealmAliasClaimPayload{ServerID: serverID, Alias: alias})
	if err != nil {
		return nil, err
	}
	var result contract.RealmAliasClaimResult
	return &result, contract.DecodeResult(resp.Result, &result)
}

// do is the internal helper: builds envelope, calls Do, unwraps error responses.
func (c *Client) do(ctx context.Context, command, repoID string, payload any) (contract.Response, error) {
	req := c.newReq(command, repoID, payload)
	resp, err := c.Do(ctx, req)
	if err != nil {
		return contract.Response{}, err
	}
	if resp.Status != contract.StatusOK {
		return resp, responseErr(resp)
	}
	return resp, nil
}

func (c *Client) newReq(command, repoID string, payload any) contract.Request {
	raw := json.RawMessage("{}")
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			raw = b
		}
	}
	return contract.Request{
		Protocol:  contract.Protocol,
		RequestID: uuid.New().String(),
		ClientID:  c.clientID,
		Command:   command,
		RepoID:    repoID,
		Payload:   raw,
	}
}

// Subscribe sends an events.subscribe request and returns a channel that receives
// events from the daemon until ctx is cancelled or the server closes the stream.
// The channel is closed when the stream ends; callers should range over it.
func (c *Client) Subscribe(ctx context.Context) (<-chan contract.Event, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable (%s): %w", c.sockPath, err)
	}

	req := c.newReq(contract.CmdEventsSubscribe, "", nil)

	// One decoder for both the subscribe ACK and all subsequent event frames.
	dec := json.NewDecoder(bufio.NewReader(conn))
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	fail := func(err error) (<-chan contract.Event, error) {
		close(done)
		_ = conn.Close()
		return nil, err
	}

	// Bound the handshake by the caller's deadline, or by the client timeout
	// when the context has no deadline.
	dl, hasDL := ctx.Deadline()
	if !hasDL {
		dl = time.Now().Add(c.timeout)
	}
	_ = conn.SetDeadline(dl)
	handshakeError := func(operation string, ioErr error) error {
		cause := ctx.Err()
		// The socket deadline and context timer share the same instant. The
		// socket can wake a few microseconds before ctx.Err becomes observable.
		if cause == nil && hasDL && !time.Now().Before(dl) {
			cause = context.DeadlineExceeded
		}
		if cause != nil {
			return fmt.Errorf("subscribe %s: %w", operation, cause)
		}
		return fmt.Errorf("subscribe %s: %w", operation, ioErr)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fail(handshakeError("send", err))
	}

	var resp contract.Response
	if err := dec.Decode(&resp); err != nil {
		return fail(handshakeError("recv", err))
	}
	if resp.Protocol != contract.Protocol || resp.RequestID != req.RequestID {
		return fail(fmt.Errorf("subscribe: bad response envelope (protocol=%q request_id=%q)", resp.Protocol, resp.RequestID))
	}
	if resp.Status != contract.StatusOK {
		if resp.Status == contract.StatusError {
			return fail(responseErr(resp))
		}
		return fail(fmt.Errorf("subscribe: unknown status %q", resp.Status))
	}
	var ack struct {
		Streaming bool `json:"streaming"`
	}
	if err := contract.DecodeResult(resp.Result, &ack); err != nil || !ack.Streaming {
		return fail(fmt.Errorf("subscribe: invalid streaming acknowledgement"))
	}

	// Clear the handshake deadline for the long-lived stream. Re-check the
	// context afterwards so cancellation cannot be lost racing with this clear.
	_ = conn.SetDeadline(time.Time{})
	if ctx.Err() != nil {
		_ = conn.SetDeadline(time.Now())
		return fail(fmt.Errorf("subscribe: %w", ctx.Err()))
	}

	ch := make(chan contract.Event, 64)
	go func() {
		defer conn.Close()
		defer close(ch)
		defer close(done)

		for {
			var ev contract.Event
			if err := dec.Decode(&ev); err != nil {
				return
			}
			if err := ev.Validate(); err != nil {
				return
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func responseErr(resp contract.Response) error {
	if resp.Error != nil {
		body := *resp.Error
		if resp.Error.Details != nil {
			body.Details = make(map[string]string, len(resp.Error.Details))
			for key, value := range resp.Error.Details {
				body.Details[key] = value
			}
		}
		return &ResponseError{Body: body}
	}
	return fmt.Errorf("error response from daemon")
}
