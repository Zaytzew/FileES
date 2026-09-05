package passport

import (
	"context"
	"errors"
)

// ConditionalSVNBackend replaces a held token without ever using svn --force.
// PrepareReplacement must call an authenticated server mutation authority that
// conditionally releases Metadata.PreviousToken. A successful prepare is not a
// reservation: another writer may win before Lock, and that failure is final.
// There is deliberately no fallback to the legacy force-lock backend.
type ConditionalSVNBackend struct {
	SVNBackend
	PrepareReplacement func(context.Context, string, Metadata) error
}

func (b ConditionalSVNBackend) Lock(ctx context.Context, path, comment string, replace bool) (*Lock, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if b.Client == nil {
		return nil, "", errors.New("conditional passport SVN client unavailable")
	}
	if replace {
		metadata, ok := ParseComment(comment)
		if !ok || metadata.PreviousToken == "" {
			return nil, "", errors.New("conditional passport replacement requires previous token")
		}
		if b.PrepareReplacement == nil {
			return nil, "", errors.New("conditional passport replacement authority unavailable")
		}
		if err := b.PrepareReplacement(ctx, path, metadata); err != nil {
			return nil, "", err
		}
	}
	// In particular, an error here after prepare MUST NOT trigger a forced retry.
	return b.SVNBackend.Lock(ctx, path, comment, false)
}
