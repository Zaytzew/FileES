package v1

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const TicketCancelPassportPreparation TicketType = "CANCEL_PASSPORT_PREPARATION"

type CancelPassportPreparationPayload struct {
	Preparation Ticket `json:"preparation"`
}

func (p CancelPassportPreparationPayload) Validate() error {
	// Check type BEFORE Validate: nested cancellations must never recurse.
	if p.Preparation.Type != TicketPreparePassportReplacement {
		return errors.New("cancellation requires an original preparation")
	}
	return p.Preparation.Validate()
}

type CancelPassportPreparationResult struct {
	PreparationOperationID string `json:"preparation_operation_id"`
	PreparationRequestID   string `json:"preparation_request_id"`
	State                  string `json:"state"`
}

func (p CancelPassportPreparationResult) Validate() error {
	if err := validateUUID("preparation_operation_id", p.PreparationOperationID); err != nil {
		return err
	}
	if err := validateUUID("preparation_request_id", p.PreparationRequestID); err != nil {
		return err
	}
	if p.State != "canceled" {
		return errors.New("preparation cancellation is not a lock receipt")
	}
	return nil
}

func NewPassportCancellation(original Ticket) (Ticket, error) {
	if err := (CancelPassportPreparationPayload{original}).Validate(); err != nil {
		return Ticket{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, original.CreatedAt)
	if err != nil {
		return Ticket{}, err
	}
	op := uuid.NewSHA1(uuid.NameSpaceOID, []byte("filees.cancel-passport.operation/"+original.OperationID)).String()
	req := uuid.NewSHA1(uuid.NameSpaceOID, []byte("filees.cancel-passport.request/"+original.RequestID)).String()
	return NewTicket(op, req, TicketCancelPassportPreparation, original.ClientID, CancelPassportPreparationPayload{original}, created)
}
