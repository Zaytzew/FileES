package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

const (
	HelperSchema           = "filees.deploy-helper/v1"
	HelperCommand          = "filees-deploy/v1"
	ActionGenerateIdentity = "generate_identity"
)

type HelperRequest struct {
	Schema      string `json:"schema"`
	OperationID string `json:"operation_id"`
	RequestID   string `json:"request_id"`
	ClientID    string `json:"client_id"`
	Action      string `json:"action"`
}

type HelperResponse struct {
	Schema      string          `json:"schema"`
	OperationID string          `json:"operation_id"`
	RequestID   string          `json:"request_id"`
	Status      string          `json:"status"`
	Identity    *PublicIdentity `json:"identity,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// PublicIdentity is the only identity material exposed to the deployment
// worker. Local state and private-key paths remain client-private.
type PublicIdentity struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

func (r HelperRequest) validate(operationID, clientID string) error {
	if r.Schema != HelperSchema {
		return fmt.Errorf("unsupported helper schema %q", r.Schema)
	}
	if _, err := uuid.Parse(r.OperationID); err != nil {
		return fmt.Errorf("operation_id must be UUID: %w", err)
	}
	if operationID != "" && r.OperationID != operationID {
		return errors.New("operation_id does not match active tunnel")
	}
	if r.ClientID == "" {
		return errors.New("client_id is required")
	}
	if clientID != "" && r.ClientID != clientID {
		return errors.New("client_id does not match active tunnel")
	}
	if _, err := uuid.Parse(r.RequestID); err != nil {
		return fmt.Errorf("request_id must be UUID: %w", err)
	}
	if r.Action != ActionGenerateIdentity {
		return fmt.Errorf("unsupported helper action %q", r.Action)
	}
	return nil
}

func decodeHelperRequest(decoder *json.Decoder, operationID, clientID string) (HelperRequest, error) {
	decoder.DisallowUnknownFields()
	var request HelperRequest
	if err := decoder.Decode(&request); err != nil {
		return HelperRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return HelperRequest{}, errors.New("helper request contains trailing JSON")
		}
		return HelperRequest{}, fmt.Errorf("decode trailing helper input: %w", err)
	}
	if err := request.validate(operationID, clientID); err != nil {
		return HelperRequest{}, err
	}
	return request, nil
}
