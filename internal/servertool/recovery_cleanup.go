package servertool

import (
	"path/filepath"
	"time"

	"filees/pkg/repoworker"
)

// reapRecoveryCapabilities is called only as part of another explicit server
// action. Public capability removal precedes receipt removal, so a crash
// cannot leave an expired key authorized without a manifest to find it from.
func reapRecoveryCapabilities(resultsRoot string, now time.Time) ([]string, error) {
	manifests := repoworker.RecoveryManifestStore{Root: filepath.Join(resultsRoot, "recovery-manifests")}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(resultsRoot, "recovery-keys")}
	expired, err := manifests.Expired(now)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(expired))
	for _, operationID := range expired {
		if err := keys.Remove(operationID); err != nil {
			return removed, err
		}
		if err := manifests.RemoveExpired(operationID, now); err != nil {
			return removed, err
		}
		removed = append(removed, operationID)
	}
	return removed, nil
}
