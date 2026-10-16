package client

import (
	"context"
	"errors"
	"strings"
)

// LockReleaseReceipt is private proof, never ordinary command output. A nil
// receipt with nil error means unsupported BEFORE mutation, not legacy success.
type LockReleaseReceipt struct{ Token, State string }
type LockReleaser interface {
	ReleaseLock(context.Context, string, string, string) (*LockReleaseReceipt, string, error)
}

func (c *execClient) ReleaseLock(ctx context.Context, wc, path, token string) (*LockReleaseReceipt, string, error) {
	if !nativeWCOps(c) {
		return nil, "", nil
	}
	cache := c.featureCache
	if cache == nil {
		cache = nativeFeatures
	}
	features, err := cache.get(ctx, c.nativeSVNPath, func(ctx context.Context) (map[string]any, error) {
		return c.nativeCommand(ctx, "", c.timeout, "--version")
	})
	if err != nil {
		return nil, "", err
	}
	if !features["unlock_token_receipt_v1"] {
		return nil, "", nil
	}
	if token == "" || len(token) > 4095 || strings.ContainsAny(token, "\x00\r\n") {
		return nil, "", errors.New("invalid unlock token")
	}
	rels, err := nativeRelatives(wc, []string{path})
	if err != nil || len(rels) != 1 {
		return nil, "", errors.New("unlock requires one WC path")
	}
	raw, err := c.nativeRemoteInput(ctx, wc, append([]byte(token), 0), "unlock", "--wc", wc, "--token-stdin", "--", rels[0])
	if err != nil {
		return nil, "", err
	}
	if err = ctx.Err(); err != nil {
		return nil, "", err
	}
	proof, err := parseReleasedLock(raw, rels[0], token)
	if err != nil {
		return nil, "", err
	}
	return proof, nativeJSON(map[string]any{"path": rels[0], "state": proof.State}), nil
}

func parseReleasedLock(raw map[string]any, path, token string) (*LockReleaseReceipt, error) {
	r, ok := raw["released_lock"].(map[string]any)
	if !ok || r["path"] != path || r["token"] != token || (r["state"] != "released" && r["state"] != "not-held") {
		return nil, errors.New("unlock receipt binding mismatch")
	}
	return &LockReleaseReceipt{Token: token, State: r["state"].(string)}, nil
}
