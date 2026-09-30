package intake

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const receivingLockName = ".receiving-v1.lock"

// Admission, receiver creation and publication share the budget lock even
// without quotas. Only the Go HTTP receiver writes before READY; no child
// inherits its payload. READY/PROCESSING belongs to the reaper, never this GC.
func (s Store) beginReceive(channelID, uploadID string) (io.Closer, error) {
	var lease io.Closer
	err := withBudgetLock(filepath.Join(s.Root, budgetLockName), func() error {
		if err := s.sweepReceiving(); err != nil {
			return fmt.Errorf("%w: %v", ErrBudgetState, err)
		}
		if s.limited() {
			if err := s.reserveLocked(channelID, uploadID); err != nil {
				return err
			}
		} else if err := os.Mkdir(filepath.Join(s.Root, uploadID), jobDirPerm); err != nil {
			return err
		}
		var err error
		lease, err = createReceivingLease(filepath.Join(s.Root, uploadID, receivingLockName))
		return err
	})
	return lease, err
}
