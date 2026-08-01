package servertool

import (
	"errors"

	"filees/pkg/activation"
	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
)

type realmRemovalFenceReader interface {
	RealmRemovalFenceForRealm(string) (activation.RealmRemovalFence, bool, error)
}

// realmRemovalAdmission blocks new realm mutations after OTP even when a
// crashed worker released .worker.lock before the server-owned journal was
// resumed. The matching confirmation remains resumable; owner-label
// resolution is the only admitted read-only ticket.
type realmRemovalAdmission struct {
	Fences realmRemovalFenceReader
}

func (a realmRemovalAdmission) Admit(session repoworker.Session, ticket control.Ticket) error {
	if a.Fences == nil {
		return errors.New("realm removal admission service is unavailable")
	}
	fence, found, err := a.Fences.RealmRemovalFenceForRealm(session.RealmID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if ticket.Type == control.TicketRealmRemoveConfirm && ticket.OperationID == fence.OperationID {
		return nil
	}
	if ticket.Type == control.TicketResolveOwnerLabels {
		return nil
	}
	return errors.New("authenticated realm is being removed")
}
