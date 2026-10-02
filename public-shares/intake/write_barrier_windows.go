package intake

import "context"

// The public host is Unix; Windows tests keep the existing fail-closed lock.
func WithReceiveBarrier(ctx context.Context, root string, _ bool, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return withBudgetLock(root+"/.capacity-receivers.lock", run)
}
