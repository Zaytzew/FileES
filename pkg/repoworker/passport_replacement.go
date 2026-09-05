package repoworker

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/passport"
	"github.com/google/uuid"
)

var (
	ErrPassportReplacementDenied = errors.New("passport replacement is not authorized")
	ErrPassportReplacementStale  = errors.New("passport replacement token is no longer current")
	ErrPathOwnerUnavailable      = errors.New("authoritative path owner is unavailable")
)

// PassportPathOwners must read server-owned facts for the current path
// incarnation. Neither a client-supplied property nor repo_owner is a fallback.
type PassportPathOwners interface {
	PathOwner(context.Context, string, string) (string, error)
}

type PassportReplacement struct {
	RepoID, Path, ObservedToken string
	PassportID, InstanceUID     string
	Mode                        string // "renew" the same passport, or "migrate" to a new one
}

// PassportReplacementAuthority is a server-only mutation boundary. Its caller
// must hold the existing worker/service-WC locks across authorization and
// mutation, and apply the realm-removal admission fence. It is NOT a broker.
// This prepares a subsequent non-force client lock; it does not promise an
// atomic transfer or grant a replacement lock token.
type PassportReplacementAuthority struct {
	Locks      SVNAdminLockAuthority
	ServiceWC  string
	PathOwners PassportPathOwners
	Now        func() time.Time
}

func (a PassportReplacementAuthority) Prepare(ctx context.Context, session Session, req PassportReplacement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.Mode != "renew" && req.Mode != "migrate" {
		return ErrPassportReplacementDenied
	}
	if _, err := a.Locks.repositoryPath(req.RepoID, req.Path); err != nil {
		return err
	}
	if err := validateObservedLockID(req.ObservedToken); err != nil {
		return err
	}
	for _, id := range []string{req.PassportID, req.InstanceUID, session.ClientID, session.RealmID} {
		if _, err := uuid.Parse(id); err != nil {
			return ErrPassportReplacementDenied
		}
	}
	if !filepath.IsAbs(a.ServiceWC) || !sessionCanWriteRepository(session, req.RepoID) {
		return ErrPassportReplacementDenied
	}
	requester, err := a.client(session.ClientID)
	if err != nil || requester.State != "active" || requester.RealmID != session.RealmID {
		return ErrPassportReplacementDenied
	}
	realm, err := readRealmRecord(filepath.Join(a.ServiceWC, "admin", "realms", session.RealmID+".json"))
	if err != nil || realm.Schema != "filees.realm/v1" || realm.RealmID != session.RealmID || realm.State != "active" {
		return ErrPassportReplacementDenied
	}
	publisher := ServicePublisher{ServiceWC: a.ServiceWC}
	repo, err := publisher.loadActiveRepository(req.RepoID)
	if err != nil {
		return ErrPassportReplacementDenied
	}
	// A stale view cannot retain rights after a grant has been revoked.
	if repo.OwnerRealmID != session.RealmID {
		if _, err := uuid.Parse(repo.OwnerRealmID); err != nil {
			return ErrPassportReplacementDenied
		}
		ownerRealm, err := readRealmRecord(filepath.Join(a.ServiceWC, "admin", "realms", repo.OwnerRealmID+".json"))
		if err != nil || ownerRealm.Schema != "filees.realm/v1" || ownerRealm.RealmID != repo.OwnerRealmID || ownerRealm.State != "active" {
			return ErrPassportReplacementDenied
		}
		grantPath, err := realmGrantPath(a.ServiceWC, req.RepoID, session.RealmID)
		if err != nil {
			return ErrPassportReplacementDenied
		}
		var grant RealmGrantRecord
		if decodeJSONFile(grantPath, &grant) != nil || validateRealmGrantRecord(grant) != nil ||
			grant.RepoID != req.RepoID || grant.OwnerRealmID != repo.OwnerRealmID ||
			grant.RecipientRealmID != session.RealmID || grant.State != "active" || grant.Access != "rw" {
			return ErrPassportReplacementDenied
		}
	}
	lock, err := a.Locks.inspectSVNLock(ctx, req.RepoID, req.Path)
	if err != nil {
		return err
	}
	if lock == nil || lock.Token != req.ObservedToken {
		return ErrPassportReplacementStale
	}
	metadata, ok := passport.ParseComment(lock.Comment)
	if !ok {
		return ErrPassportReplacementDenied
	}
	for _, id := range []string{metadata.PassportID, metadata.InstanceUID} {
		if _, err := uuid.Parse(id); err != nil {
			return ErrPassportReplacementDenied
		}
	}
	if metadata.IssuedAt.IsZero() || !metadata.ExpiresAt.After(metadata.IssuedAt) || metadata.HardExpiresAt.Before(metadata.ExpiresAt) {
		return ErrPassportReplacementDenied
	}
	switch req.Mode {
	case "renew":
		// Possession of a token alone never authorizes renewing somebody else's
		// installation, even inside the same realm.
		if lock.Owner != session.ClientID || metadata.PassportID != req.PassportID || metadata.InstanceUID != req.InstanceUID {
			return ErrPassportReplacementDenied
		}
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		if !now.Before(metadata.ExpiresAt) || !now.Before(metadata.HardExpiresAt) {
			return ErrPassportReplacementDenied
		}
	case "migrate":
		if a.PathOwners == nil {
			return ErrPathOwnerUnavailable
		}
		owner, err := a.PathOwners.PathOwner(ctx, req.RepoID, req.Path)
		if err != nil {
			return err
		}
		if owner == "" {
			return ErrPathOwnerUnavailable
		}
		if owner != session.RealmID {
			return ErrPassportReplacementDenied
		}
		holder, err := a.client(lock.Owner)
		// A revoked installation does not erase its realm identity. This permits
		// owner recovery from another active installation, but never across realms.
		if err != nil || holder.RealmID != session.RealmID {
			return ErrPassportReplacementDenied
		}
		// Realm in the editable SVN comment is deliberately ignored.
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.Locks.unlockIfCurrent(ctx, req.RepoID, req.Path, lock.Owner, req.ObservedToken)
}

type passportClientIdentity struct {
	Schema   string `json:"schema"`
	ClientID string `json:"client_id"`
	RealmID  string `json:"realm_id"`
	State    string `json:"state"`
}

func (a PassportReplacementAuthority) client(clientID string) (passportClientIdentity, error) {
	var result passportClientIdentity
	if !filepath.IsAbs(a.ServiceWC) {
		return result, ErrPassportReplacementDenied
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return result, ErrPassportReplacementDenied
	}
	if err := decodeJSONFile(filepath.Join(a.ServiceWC, "admin", "clients", clientID+".json"), &result); err != nil {
		return result, err
	}
	if result.Schema != "filees.client-instance/v1" || result.ClientID != clientID || (result.State != "active" && result.State != "revoked") {
		return passportClientIdentity{}, ErrPassportReplacementDenied
	}
	if _, err := uuid.Parse(result.RealmID); err != nil {
		return passportClientIdentity{}, ErrPassportReplacementDenied
	}
	return result, nil
}
