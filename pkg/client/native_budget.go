package client

import "context"

// Process-wide, not per WC. Remote work leaves two slots for local inspection.
// Slots cover the whole child lifetime (including pipe drain), not just Start.
// Admission does not consume the child's execution timeout; the caller's
// deadline still bounds the wait. This never retries an operation.
type nativeProcessBudget struct {
	all, remote chan struct{}
}

func newNativeProcessBudget() *nativeProcessBudget {
	return &nativeProcessBudget{make(chan struct{}, 4), make(chan struct{}, 2)}
}

var nativeProcesses = newNativeProcessBudget()

type nativeRemoteContextKey struct{}

func (b *nativeProcessBudget) acquire(ctx context.Context, remote bool) (func(), error) {
	take := func(ch chan struct{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case ch <- struct{}{}:
			if err := ctx.Err(); err != nil {
				<-ch
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if remote {
		if err := take(b.remote); err != nil {
			return nil, err
		}
	}
	if err := take(b.all); err != nil {
		if remote {
			<-b.remote
		}
		return nil, err
	}
	return func() {
		<-b.all
		if remote {
			<-b.remote
		}
	}, nil
}
