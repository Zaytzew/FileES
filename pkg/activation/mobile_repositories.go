package activation

import (
	"path/filepath"
	"time"

	"filees/pkg/onboarding"
)

// canonicalRepositorySchema mirrors pkg/repoworker.RepositorySchema. Not
// imported directly: pkg/activation and pkg/repoworker are peer packages
// with no existing import edge between them, and this package already
// treats every service-working-copy JSON file as its own local contract
// (raw map[string]any throughout this file) rather than importing the
// writer's package.
const canonicalRepositorySchema = "filees.repository/v1"

// canonicalRepositoryRecord mirrors the on-disk shape written by
// repoworker.ServicePublisher.Publish/Activate at
// admin/repositories/<repoID>.json.
// This mirror is decoded by readStrict, which rejects unknown fields, so it
// must stay in lockstep with the writer's struct. A field added there and
// forgotten here does not fail loudly: the read errors, the caller's
// `continue` skips the repository, and the phone pairs with a silently
// short list.
type canonicalRepositoryRecord struct {
	Schema        string    `json:"schema"`
	RepoID        string    `json:"repo_id"`
	OwnerRealmID  string    `json:"owner_realm_id"`
	DisplayName   string    `json:"display_name"`
	URL           string    `json:"url"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	EditingPolicy string    `json:"editing_policy,omitempty"`
}

// mobileRepositoryEntries builds the "repositories" array for a freshly
// activated client's view.json. Every non-mobile record (and every mobile
// record with no inherited grants) gets an empty list, unchanged from prior
// behavior. A mobile record with inherited grants gets each entry rebuilt
// from the canonical repository record on disk (DisplayName/URL/State read
// fresh, not snapshotted at pairing-mint time - State in particular may
// have moved on from "initializing" to "active" during the pairing TTL
// window), carrying over only Access/AttachmentPolicy from the grant
// itself (per-client data the canonical record does not have). A repo
// whose canonical record has since disappeared or gone invalid is skipped
// rather than failing the whole activation - a deleted/renamed repository
// must not block a phone from pairing at all.
func (m *Manager) mobileRepositoryEntries(record Record) ([]any, error) {
	entries := make([]any, 0, len(record.Repositories))
	if record.Kind != onboarding.KindMobile || len(record.Repositories) == 0 {
		return entries, nil
	}
	for _, grant := range record.Repositories {
		path := filepath.Join(m.config.ServiceWorkingCopy, "admin", "repositories", grant.RepoID+".json")
		var canonical canonicalRepositoryRecord
		if err := readStrict(path, canonicalRepositorySchema, &canonical); err != nil {
			continue
		}
		if canonical.RepoID != grant.RepoID {
			continue
		}
		// editing_policy is deliberately absent: the mobile channel is
		// append-only and does not take part in edit passports at all, so
		// projecting a policy it can neither honour nor act on would only
		// invite a phone to render a barrier that does not apply to it.
		entries = append(entries, map[string]any{
			"repo_id": canonical.RepoID, "display_name": canonical.DisplayName, "url": canonical.URL,
			"access": grant.Access, "state": canonical.State, "owner_realm_id": canonical.OwnerRealmID,
			"attachment_policy": attachmentPolicyOrDefault(grant.AttachmentPolicy),
		})
	}
	return entries, nil
}

func attachmentPolicyOrDefault(policy string) string {
	if policy == "" {
		return "optional"
	}
	return policy
}
