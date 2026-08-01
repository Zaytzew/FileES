package activation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const RealmRemovalFenceSchema = "filees.realm-removal-fence/v1"

// RealmRemovalFence is the durable admission barrier established immediately
// after OTP confirmation. It prevents an activation from entering a realm
// while its immutable deletion snapshot is being archived and revoked.
type RealmRemovalFence struct {
	Schema      string    `json:"schema"`
	OperationID string    `json:"operation_id"`
	RealmID     string    `json:"realm_id"`
	ClientIDs   []string  `json:"client_ids"`
	CreatedAt   time.Time `json:"created_at"`
}

// RealmRemovalFenceForRealm returns the durable admission barrier used by
// authenticated control-plane dispatch. Corrupt fence state is an error, not
// an absent fence, so callers fail closed.
func (m *Manager) RealmRemovalFenceForRealm(realmID string) (RealmRemovalFence, bool, error) {
	if _, err := uuid.Parse(realmID); err != nil {
		return RealmRemovalFence{}, false, errors.New("realm removal fence realm_id must be UUID")
	}
	var fence RealmRemovalFence
	err := withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		var err error
		fence, err = m.loadRealmRemovalFence(realmID)
		return err
	})
	if errors.Is(err, os.ErrNotExist) {
		return RealmRemovalFence{}, false, nil
	}
	return fence, err == nil, err
}

// FenceRealmRemoval binds the exact credential snapshot before any active
// repository is deleted. Replays of the same operation are idempotent; a
// different operation can never reopen or replace the fence.
func (m *Manager) FenceRealmRemoval(realmID, operationID string, clientIDs []string) error {
	expected, err := validateRealmRemovalFenceIDs(realmID, operationID, clientIDs)
	if err != nil {
		return err
	}
	return withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		if existing, err := m.loadRealmRemovalFence(realmID); err == nil {
			if existing.OperationID != operationID || !equalIDs(existing.ClientIDs, expected) {
				return errors.New("realm removal conflicts with existing admission fence")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		records, err := m.recordsLocked()
		if err != nil {
			return err
		}
		if err := validateRealmRemovalRecords(records, realmID, expected, ""); err != nil {
			return err
		}
		fence := RealmRemovalFence{
			Schema: RealmRemovalFenceSchema, OperationID: operationID, RealmID: realmID,
			ClientIDs: expected, CreatedAt: m.now().UTC(),
		}
		return atomicWriteJSON(m.realmRemovalFencePath(realmID), fence, 0o600)
	})
}

// RevokeRealmRemoval revokes exactly the clients authorized by the durable
// fence. Historical revoked clients are ignored, while any new live client is
// a fail-closed scope conflict rather than an implicit expansion.
func (m *Manager) RevokeRealmRemoval(ctx context.Context, realmID, operationID string, clientIDs []string, reason string) ([]string, error) {
	expected, err := validateRealmRemovalFenceIDs(realmID, operationID, clientIDs)
	if err != nil {
		return nil, err
	}
	reason, err = validateRevokeReason(reason)
	if err != nil {
		return nil, err
	}
	var targets []string
	err = withFileLock(filepath.Join(m.config.Root, ".activation.lock"), func() error {
		fence, err := m.loadRealmRemovalFence(realmID)
		if err != nil {
			return err
		}
		if fence.OperationID != operationID || !equalIDs(fence.ClientIDs, expected) {
			return errors.New("realm removal admission fence does not match operation")
		}
		records, err := m.recordsLocked()
		if err != nil {
			return err
		}
		if err := validateRealmRemovalRecords(records, realmID, expected, reason); err != nil {
			return err
		}
		targetSet := idSet(expected)
		for _, record := range records {
			if !targetSet[record.ClientID] || record.State == "expired" || record.State == "revoked" {
				continue
			}
			if record.State == "staged" || record.State == "active" {
				now := m.now().UTC()
				record.State, record.RevokedAt, record.RevokeReason = "revoking", &now, reason
				if err := atomicWriteJSON(m.recordPath(record.OperationID), record, 0o600); err != nil {
					return err
				}
			}
			targets = append(targets, record.ClientID)
		}
		return m.renderAccessLocked()
	})
	if err != nil {
		return nil, err
	}
	for _, clientID := range targets {
		_ = signalSessionLeases(m.sessionRoot(), clientID, "")
	}
	revoked := make([]string, 0, len(targets))
	for _, clientID := range targets {
		if _, err := m.Revoke(ctx, clientID, reason); err != nil {
			return revoked, fmt.Errorf("revoke client %s: %w", clientID, err)
		}
		revoked = append(revoked, clientID)
	}
	return revoked, nil
}

func validateRealmRemovalFenceIDs(realmID, operationID string, clientIDs []string) ([]string, error) {
	if _, err := uuid.Parse(realmID); err != nil {
		return nil, errors.New("realm removal fence realm_id must be UUID")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return nil, errors.New("realm removal fence operation_id must be UUID")
	}
	ids := append([]string(nil), clientIDs...)
	sort.Strings(ids)
	for index, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return nil, errors.New("realm removal fence client_id must be UUID")
		}
		if index > 0 && id == ids[index-1] {
			return nil, errors.New("realm removal fence contains duplicate client_id")
		}
	}
	return ids, nil
}

func validateRealmRemovalRecords(records []Record, realmID string, expected []string, revokeReason string) error {
	targets := idSet(expected)
	found := map[string]bool{}
	for _, record := range records {
		if record.RealmID != realmID {
			continue
		}
		if targets[record.ClientID] {
			found[record.ClientID] = true
			switch record.State {
			case "staged", "active":
			case "revoking":
				if revokeReason == "" {
					return fmt.Errorf("realm removal client %s is already being revoked", record.ClientID)
				}
				if record.RevokeReason != revokeReason {
					return fmt.Errorf("realm removal client %s has conflicting revoke reason", record.ClientID)
				}
			case "expired", "revoked":
				// The exact snapshotted credential is already closed. Its earlier
				// expiry or administrative reason does not expand or weaken this
				// operation.
			default:
				return fmt.Errorf("realm removal client %s is not revocable", record.ClientID)
			}
			continue
		}
		if record.State == "staged" || record.State == "active" || record.State == "revoking" {
			return errors.New("realm removal credential snapshot changed")
		}
	}
	for _, id := range expected {
		if !found[id] {
			return fmt.Errorf("realm removal client %s is missing", id)
		}
	}
	return nil
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func equalIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (m *Manager) loadRealmRemovalFence(realmID string) (RealmRemovalFence, error) {
	var fence RealmRemovalFence
	if err := readStrict(m.realmRemovalFencePath(realmID), RealmRemovalFenceSchema, &fence); err != nil {
		return RealmRemovalFence{}, err
	}
	if fence.RealmID != realmID || fence.CreatedAt.IsZero() {
		return RealmRemovalFence{}, errors.New("realm removal admission fence is invalid")
	}
	ids, err := validateRealmRemovalFenceIDs(fence.RealmID, fence.OperationID, fence.ClientIDs)
	if err != nil || !equalIDs(ids, fence.ClientIDs) {
		return RealmRemovalFence{}, errors.New("realm removal admission fence is invalid")
	}
	return fence, nil
}

func (m *Manager) realmRemovalFencePath(realmID string) string {
	return filepath.Join(m.config.Root, "realm-removal-fences", realmID+".json")
}
