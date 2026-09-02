package repoworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// CompleteCanonicalRecord fills the canonical fields an older repository record
// predates, and refuses the ones that cannot be derived without inventing them.
// It exists for repair tooling, not for the live create path, which always
// writes a complete record.
//
// The stake is higher than one repository. clientview.View.Validate rejects the
// WHOLE view when any entry has an empty display_name or a URL that is not
// svn+ssh, and the client decodes its projection strictly and exits on failure.
// So publishing one incomplete record does not degrade that repository - it
// makes every repository disappear for every client that receives the view.
// Activation must therefore never emit a record it has not first completed.
//
// Returned healed names the fields it filled, so the operator sees what the
// repair changed rather than having to diff the record afterwards. The caller
// publishes: this function only writes the working copy.
func (p ServicePublisher) CompleteCanonicalRecord(repoID, urlPrefix string) (path string, healed []string, err error) {
	path, err = repositoryRecordPath(p.ServiceWC, repoID)
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	// Deliberately lenient where Activate is strict: the point is to read a
	// record written by an older build and bring it up to the current shape.
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", nil, fmt.Errorf("canonical repository record is unreadable: %w", err)
	}
	if record.RepoID != "" && record.RepoID != repoID {
		return "", nil, errors.New("canonical repository record carries a different repo_id")
	}
	if record.Schema != "" && record.Schema != RepositorySchema {
		return "", nil, fmt.Errorf("canonical repository record has unsupported schema %q", record.Schema)
	}
	// These two carry meaning no tool can reconstruct. display_name is the
	// name a human chose and sees; owner_realm_id decides who may act on the
	// repository, and taking it from an operator flag would quietly turn a
	// repair into a grant. repo transfer-owner is the command for that.
	if record.DisplayName == "" {
		return "", nil, errors.New("canonical repository record has no display_name; it cannot be derived and must be restored first")
	}
	if record.OwnerRealmID == "" {
		return "", nil, errors.New("canonical repository record has no owner_realm_id; use repo transfer-owner to establish ownership first")
	}
	if record.Schema == "" {
		record.Schema = RepositorySchema
		healed = append(healed, "schema")
	}
	if record.RepoID == "" {
		record.RepoID = repoID
		healed = append(healed, "repo_id")
	}
	if record.URL == "" {
		url, err := repositoryURL(urlPrefix, repoID)
		if err != nil {
			return "", nil, fmt.Errorf("derive repository url: %w", err)
		}
		record.URL = url
		healed = append(healed, "url")
	}
	if record.CreatedAt.IsZero() {
		// The record's own mtime is the closest honest evidence of when the
		// repository came into being; inventing "now" would date an 18-day-old
		// repository to the moment somebody repaired it.
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, err
		}
		record.CreatedAt = info.ModTime().UTC()
		healed = append(healed, "created_at")
	}
	// EditingPolicy and Purpose are intentionally absent, not missing: the
	// empty value IS the default, and clientview requires that the default is
	// never serialised. Writing them here would be a regression, not a repair.
	if len(healed) == 0 {
		return path, nil, nil
	}
	if err := atomicJSON(path, record); err != nil {
		return "", nil, err
	}
	return path, healed, nil
}
