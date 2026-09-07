package repoworker

import "path/filepath"

// Cleanup checks activation and canonical realm, not a revoked repository
// grant. Callers must bind the durable attempt to this actor before any effect.
// Transport/realm-removal admission and the service-WC lock remain required.
func (a PassportReplacementAuthority) authorizePassportCleanup(session Session) error {
	if !filepath.IsAbs(a.ServiceWC) {
		return ErrPassportReplacementDenied
	}
	actor, err := a.client(session.ClientID)
	if err != nil || actor.RealmID != session.RealmID || actor.State != "active" {
		return ErrPassportReplacementDenied
	}
	realm, err := readRealmRecord(filepath.Join(a.ServiceWC, "admin", "realms", session.RealmID+".json"))
	if err != nil || realm.Schema != "filees.realm/v1" || realm.RealmID != session.RealmID || realm.State != "active" {
		return ErrPassportReplacementDenied
	}
	return nil
}
