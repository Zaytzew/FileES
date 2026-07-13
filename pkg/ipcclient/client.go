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
)

const defaultTimeout = 10 * time.Second

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
	// Honour the caller's context deadline if it is longer than c.timeout.
	// This lets lock/unlock (30 s context) use a full 30 s instead of being
	// capped at the 10 s client default.
	dl := time.Now().Add(c.timeout)
	if ctxDl, ok := ctx.Deadline(); ok && ctxDl.After(dl) {
		dl = ctxDl
	}
	_ = conn.SetDeadline(dl)

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
	if err != nil { return nil, err }
	var r contract.HelloResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) SystemStatus(ctx context.Context) (*contract.SystemStatusResult, error) {
	resp, err := c.do(ctx, contract.CmdSystemStatus, "", nil)
	if err != nil { return nil, err }
	var r contract.SystemStatusResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) RepoList(ctx context.Context) (*contract.RepoListResult, error) {
	resp, err := c.do(ctx, contract.CmdRepoList, "", nil)
	if err != nil { return nil, err }
	var r contract.RepoListResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) RepoStatus(ctx context.Context, repoID string) (*contract.RepoStatus, error) {
	resp, err := c.do(ctx, contract.CmdRepoStatus, repoID, nil)
	if err != nil { return nil, err }
	var r contract.RepoStatus
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) ErrorList(ctx context.Context, pl contract.ErrorListPayload) (*contract.ErrorListResult, error) {
	resp, err := c.do(ctx, contract.CmdErrorList, pl.RepoID, pl)
	if err != nil { return nil, err }
	var r contract.ErrorListResult
	return &r, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) Lock(ctx context.Context, repoID string, paths []string) (string, error) {
	resp, err := c.do(ctx, contract.CmdRepoLock, repoID, contract.RepoLockPayload{Paths: paths})
	if err != nil { return "", err }
	var r contract.LockResult
	return r.Output, contract.DecodeResult(resp.Result, &r)
}

func (c *Client) Unlock(ctx context.Context, repoID string, paths []string) (string, error) {
	resp, err := c.do(ctx, contract.CmdRepoUnlock, repoID, contract.RepoLockPayload{Paths: paths})
	if err != nil { return "", err }
	var r contract.LockResult
	return r.Output, contract.DecodeResult(resp.Result, &r)
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

func responseErr(resp contract.Response) error {
	if resp.Error != nil {
		return fmt.Errorf("[%s] %s", resp.Error.Code, resp.Error.MessageKey)
	}
	return fmt.Errorf("error response from daemon")
}
