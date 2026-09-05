package repoworker

import (
	"encoding/json"
	"os"

	"github.com/google/uuid"
)

// RepositoryDeletedForRealm reads existing canonical authority, including
// historical tombstones. It reveals only deletion, only to an owner or a
// realm with a durable past grant. It grants no access to files or locks.
// Missing/malformed records and unknown realms never establish deletion.
func RepositoryDeletedForRealm(serviceWC, repoID, realmID string) bool {
	if _, err := uuid.Parse(repoID); err != nil {
		return false
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return false
	}
	path, err := repositoryRecordPath(serviceWC, repoID)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var record repositoryRecord
	if json.Unmarshal(raw, &record) != nil || record.Schema != RepositorySchema ||
		record.RepoID != repoID || record.State != "deleted" {
		return false
	}
	if record.OwnerRealmID == realmID {
		return true
	}
	path, err = realmGrantPath(serviceWC, repoID, realmID)
	if err != nil {
		return false
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		return false
	}
	var grant RealmGrantRecord
	return json.Unmarshal(raw, &grant) == nil && validateRealmGrantRecord(grant) == nil &&
		grant.RepoID == repoID && grant.RecipientRealmID == realmID &&
		grant.OwnerRealmID == record.OwnerRealmID
}
