package client

import (
	"context"
	"errors"
)

// LockAcquirer returns a one-shot receipt for this invocation, not a cached
// observation. nil proof means legacy success: the caller must reconcile.
// Any error (including lost reply) forbids using a partial receipt.
type LockAcquirer interface {
	AcquireLock(context.Context, string, string, string) (*LockInfo, string, error)
}

func (c *execClient) AcquireLock(ctx context.Context, wc, path, comment string) (*LockInfo, string, error) {
	if !nativeWCOps(c) {
		out, err := c.LockWithComment(ctx, wc, []string{path}, comment, false)
		return nil, out, err
	}
	rows, err := c.nativeLockResults(ctx, wc, []string{path}, "lock", comment)
	out := nativeJSON(map[string]any{"results": rows})
	if err != nil {
		// Preserve missing-source recovery, but never turn it into a mutation
		// receipt: it requires the existing independent reconciliation path.
		out, err = c.recoverMissingPathLock(ctx, wc, []string{path}, comment, out, err)
		return nil, out, err
	}
	if err = ctx.Err(); err != nil {
		return nil, out, err
	}
	if len(rows) != 1 {
		return nil, out, errors.New("acquisition requires one receipt")
	}
	proof := rows[0].Acquired
	if proof != nil && (comment == "" || proof.Comment != comment) {
		return nil, out, errors.New("acquisition receipt comment mismatch")
	}
	return proof, out, nil
}

func parseAcquiredLock(value any) (*LockInfo, error) {
	r, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid acquired lock")
	}
	token, tok := r["token"].(string)
	owner, own := r["owner"].(string)
	comment, com := r["comment"].(string)
	if !tok || !own || !com || token == "" || owner == "" {
		return nil, errors.New("incomplete acquired lock")
	}
	return &LockInfo{Token: token, Owner: owner, Comment: comment}, nil
}
