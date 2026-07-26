//go:build windows

package activation

import (
	"errors"
	"time"
)

// The FileES server target is Unix. Keeping an explicit stub makes accidental
// use by a future Windows server fail closed instead of silently dropping a
// revoke notification.
type SessionMetadata struct {
	Schema      string
	SessionID   string
	OperationID string
	ClientID    string
	RealmID     string
	StartedAt   time.Time
}
type SessionLease struct{ Metadata SessionMetadata }

func (lease *SessionLease) Close() error { return nil }

func (lease *SessionLease) Revoked() (bool, error) {
	return false, errors.New("supervised session leases are unsupported on windows")
}

func createSessionLease(string, SessionMetadata) (*SessionLease, error) {
	return nil, errors.New("supervised session leases are unsupported on windows")
}

func signalSessionLeases(string, string, string) error {
	return errors.New("supervised session leases are unsupported on windows")
}

func cleanupOrphanedSessionLeases(string) (int, error) {
	return 0, errors.New("supervised session lease cleanup is unsupported on windows")
}
