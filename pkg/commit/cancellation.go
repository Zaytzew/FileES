package commit

import "context"

// Only a cancellation of this caller, with no independent joined cause, is
// quiet. Deadlines, text matches and unrelated failures during stop stay visible.
func ownCancellation(ctx context.Context, err error) bool {
	return ctx.Err() == context.Canceled && cancellationOnly(err)
}

func cancellationOnly(err error) bool {
	if err == context.Canceled {
		return true
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return cancellationOnly(e.Unwrap())
	}
	return false
}

func (s *Service) recordCommitFailureContext(ctx context.Context, what string, err error) {
	if ownCancellation(ctx, err) {
		return // Presentation only: never retire the durable publication intent.
	}
	s.recordCommitFailure(what, err)
}
