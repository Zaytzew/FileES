//go:build !windows

package activation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WithRealmSessionsStopped is called under the service-WC lock, in the same
// lock order as activation publication. ClaimSession also takes the activation
// lock: no new session can capture old authz between draining and publication.
// A lease is removed by its supervisor only after the child has ended. Thus a
// commit that wins the race completes BEFORE the grant changes, never after a
// successful revoke/downgrade response. Closing all sessions of this realm is
// intentional: the raw SVN stream is not bound to a single repository.
func (m *Manager) WithRealmSessionsStopped(ctx context.Context, realmID string, publish func() error) error {
	if _, err := uuid.Parse(realmID); err != nil || publish == nil {
		return errors.New("session barrier requires realm and publication")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Do not reclaim orphaned leases here: a vanished supervisor is not
		// proof that its child finished. Missing acknowledgement fails closed.
		if err := signalSessionLeases(m.sessionRoot(), "", realmID); err != nil {
			return err
		}
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			live, err := realmSessionExists(m.sessionRoot(), realmID)
			if err != nil {
				return err
			}
			if !live {
				if err := ctx.Err(); err != nil {
					return err
				}
				return publish()
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("grant unchanged: session shutdown not acknowledged: %w", ctx.Err())
			case <-tick.C:
			}
		}
	})
}

func realmSessionExists(root, realmID string) (bool, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		metadata, err := readSessionMetadata(dir)
		if err != nil {
			// Close removes FIFO, then metadata, then the directory. Treat that
			// transient absence as live until directory removal acknowledges it.
			if _, statErr := os.Lstat(dir); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if _, statErr := os.Lstat(filepath.Join(dir, sessionRecordName)); errors.Is(statErr, os.ErrNotExist) {
				return true, nil
			}
			return false, err
		}
		if metadata.RealmID == realmID {
			return true, nil
		}
	}
	return false, nil
}
