package onboarding

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAdminRequestProtocolIsStrictAndVersioned(t *testing.T) {
	requestID := uuid.NewString()
	policy := Policy{RealmID: testRealmID}
	valid := []AdminRequest{
		{Schema: AdminProtocolSchema, RequestID: requestID, Command: AdminTicketCreate, Email: "user@example.net", Policy: &policy, TicketTTLSeconds: 3600},
		{Schema: AdminProtocolSchema, RequestID: requestID, Command: AdminTicketRevoke, TicketID: uuid.NewString()},
		{Schema: AdminProtocolSchema, RequestID: requestID, Command: AdminTicketList},
		{Schema: AdminProtocolSchema, RequestID: requestID, Command: AdminOperationInspect, OperationID: uuid.NewString()},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Errorf("valid %s request rejected: %v", request.Command, err)
		}
	}
	invalid := valid[0]
	invalid.TicketID = uuid.NewString()
	if err := invalid.Validate(); err == nil {
		t.Fatal("unrelated command field accepted")
	}
	unknown := `{"schema":"filees.onboarding-admin/v1","request_id":"` + requestID + `","command":"ticket_list","surprise":true}`
	if _, err := DecodeAdminRequest(strings.NewReader(unknown)); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
	trailing := `{"schema":"filees.onboarding-admin/v1","request_id":"` + requestID + `","command":"ticket_list"} {}`
	if _, err := DecodeAdminRequest(strings.NewReader(trailing)); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
}

func TestAdminResponseConsistency(t *testing.T) {
	requestID := uuid.NewString()
	if err := (AdminResponse{Schema: AdminProtocolSchema, RequestID: requestID, Status: AdminOK, Tickets: []Ticket{}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (AdminResponse{Schema: AdminProtocolSchema, RequestID: requestID, Status: AdminError, ErrorCode: "not_found", Message: "not found"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (AdminResponse{Schema: AdminProtocolSchema, RequestID: requestID, Status: AdminError}).Validate(); err == nil {
		t.Fatal("incomplete error response accepted")
	}
}
