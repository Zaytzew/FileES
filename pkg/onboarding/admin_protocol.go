package onboarding

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const AdminProtocolSchema = "filees.onboarding-admin/v1"

type AdminCommand string

const (
	AdminTicketCreate     AdminCommand = "ticket_create"
	AdminTicketRevoke     AdminCommand = "ticket_revoke"
	AdminTicketList       AdminCommand = "ticket_list"
	AdminOperationInspect AdminCommand = "operation_inspect"
)

type AdminRequest struct {
	Schema           string       `json:"schema"`
	RequestID        string       `json:"request_id"`
	Command          AdminCommand `json:"command"`
	Email            string       `json:"email,omitempty"`
	Policy           *Policy      `json:"policy,omitempty"`
	TicketTTLSeconds int64        `json:"ticket_ttl_seconds,omitempty"`
	TicketID         string       `json:"ticket_id,omitempty"`
	OperationID      string       `json:"operation_id,omitempty"`
}

func (request AdminRequest) Validate() error {
	if request.Schema != AdminProtocolSchema {
		return fmt.Errorf("unsupported admin protocol schema %q", request.Schema)
	}
	if _, err := uuid.Parse(request.RequestID); err != nil {
		return errors.New("admin request_id must be a UUID")
	}
	switch request.Command {
	case AdminTicketCreate:
		if _, err := canonicalEmail(request.Email); err != nil {
			return err
		}
		if request.Policy == nil {
			return errors.New("ticket_create requires policy")
		}
		if err := validatePolicy(*request.Policy); err != nil {
			return err
		}
		if request.TicketTTLSeconds <= 0 {
			return errors.New("ticket_create requires positive ticket_ttl_seconds")
		}
		if request.TicketID != "" || request.OperationID != "" {
			return errors.New("ticket_create contains unrelated fields")
		}
	case AdminTicketRevoke:
		if _, err := uuid.Parse(request.TicketID); err != nil {
			return errors.New("ticket_revoke requires UUID ticket_id")
		}
		if request.Email != "" || request.Policy != nil || request.TicketTTLSeconds != 0 || request.OperationID != "" {
			return errors.New("ticket_revoke contains unrelated fields")
		}
	case AdminTicketList:
		if request.Email != "" || request.Policy != nil || request.TicketTTLSeconds != 0 || request.TicketID != "" || request.OperationID != "" {
			return errors.New("ticket_list contains unrelated fields")
		}
	case AdminOperationInspect:
		if _, err := uuid.Parse(request.OperationID); err != nil {
			return errors.New("operation_inspect requires UUID operation_id")
		}
		if request.Email != "" || request.Policy != nil || request.TicketTTLSeconds != 0 || request.TicketID != "" {
			return errors.New("operation_inspect contains unrelated fields")
		}
	default:
		return fmt.Errorf("unsupported admin command %q", request.Command)
	}
	return nil
}

func DecodeAdminRequest(reader io.Reader) (AdminRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 64*1024))
	decoder.DisallowUnknownFields()
	var request AdminRequest
	if err := decoder.Decode(&request); err != nil {
		return AdminRequest{}, fmt.Errorf("decode admin request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AdminRequest{}, errors.New("decode admin request: trailing JSON value")
	}
	if err := request.Validate(); err != nil {
		return AdminRequest{}, err
	}
	return request, nil
}

type AdminStatus string

const (
	AdminOK    AdminStatus = "ok"
	AdminError AdminStatus = "error"
)

type AdminResponse struct {
	Schema    string      `json:"schema"`
	RequestID string      `json:"request_id"`
	Status    AdminStatus `json:"status"`
	ErrorCode string      `json:"error_code,omitempty"`
	Message   string      `json:"message,omitempty"`
	Ticket    *Ticket     `json:"ticket,omitempty"`
	Tickets   []Ticket    `json:"tickets,omitempty"`
	Operation *Operation  `json:"operation,omitempty"`
}

func (response AdminResponse) Validate() error {
	if response.Schema != AdminProtocolSchema {
		return fmt.Errorf("unsupported admin protocol schema %q", response.Schema)
	}
	if _, err := uuid.Parse(response.RequestID); err != nil {
		return errors.New("admin response request_id must be a UUID")
	}
	if response.Status == AdminError {
		if strings.TrimSpace(response.ErrorCode) == "" || strings.TrimSpace(response.Message) == "" {
			return errors.New("error response requires error_code and message")
		}
		if response.Ticket != nil || response.Tickets != nil || response.Operation != nil {
			return errors.New("error response cannot contain result")
		}
		return nil
	}
	if response.Status != AdminOK {
		return fmt.Errorf("unsupported admin response status %q", response.Status)
	}
	if response.ErrorCode != "" || response.Message != "" {
		return errors.New("successful response cannot contain error")
	}
	results := 0
	if response.Ticket != nil {
		results++
	}
	if response.Tickets != nil {
		results++
	}
	if response.Operation != nil {
		results++
	}
	if results > 1 {
		return errors.New("admin response contains multiple results")
	}
	return nil
}
