package servertool

import (
	"errors"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

// errDemoRealmExpired is the hard end of a demo realm. It is enforced at the
// door, before any control ticket or SVN session runs, so the minute between
// the TTL and the next `filees-admin demo reap` grants nothing.
var errDemoRealmExpired = errors.New("demo realm has expired")

type realmActivationReader interface {
	RealmActivatedAt(string) (time.Time, bool, error)
}

type demoRealmAdmission struct {
	Policy      serverconfig.DemoPolicy
	Activations realmActivationReader
	Now         func() time.Time
}

func (a demoRealmAdmission) Admit(session repoworker.Session, _ control.Ticket) error {
	if !a.Policy.Enabled {
		return nil
	}
	if a.Activations == nil {
		return errors.New("demo admission service is unavailable")
	}
	activatedAt, found, err := a.Activations.RealmActivatedAt(session.RealmID)
	if err != nil {
		return err
	}
	if found && a.Policy.Expired(activatedAt, a.now()) {
		return errDemoRealmExpired
	}
	return nil
}

func (a demoRealmAdmission) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// admissionChain admits a ticket only when every admission does.
type admissionChain []repoworker.SessionAdmission

func (c admissionChain) Admit(session repoworker.Session, ticket control.Ticket) error {
	for _, admission := range c {
		if err := admission.Admit(session, ticket); err != nil {
			return err
		}
	}
	return nil
}

type clientRealmActivationReader interface {
	ClientRealmActivatedAt(operationID, clientID string) (time.Time, bool, error)
}

// demoClientExpired answers the SSH entry, which knows a credential and not a
// realm. A failure to read the record refuses the session as well.
func demoClientExpired(policy serverconfig.DemoPolicy, reader clientRealmActivationReader, operationID, clientID string, now time.Time) error {
	if !policy.Enabled {
		return nil
	}
	activatedAt, found, err := reader.ClientRealmActivatedAt(operationID, clientID)
	if err != nil {
		return err
	}
	if found && policy.Expired(activatedAt, now) {
		return errDemoRealmExpired
	}
	return nil
}
