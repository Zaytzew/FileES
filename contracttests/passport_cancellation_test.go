package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"github.com/google/uuid"
)

func TestPassportCancellationWireAndReceipt(t *testing.T) {
	original, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, uuid.NewString(), control.PreparePassportReplacementPayload{RepoID: uuid.NewString(), Path: "Łódź/doc", ObservedLockID: "old-token", PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "renew"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := control.NewPassportCancellation(original)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := control.NewPassportCancellation(original)
	raw, _ := json.Marshal(cancel)
	rawAgain, _ := json.Marshal(again)
	if string(raw) != string(rawAgain) {
		t.Fatal("cancellation identity changed")
	}
	if _, err := control.ParseTicket(raw); err != nil {
		t.Fatal(err)
	}
	wrong := cancel
	wrong.ClientID = uuid.NewString()
	if err := wrong.Validate(); err == nil {
		t.Fatal("other actor allowed")
	}
	wrong = cancel
	wrong.Payload, _ = json.Marshal(control.CancelPassportPreparationPayload{Preparation: cancel})
	if err := wrong.Validate(); err == nil {
		t.Fatal("nested cancellation allowed")
	}
	for _, scenario := range []string{"canceled", "wrong-outer", "wrong-original", "acquired", "unknown", "transport"} {
		t.Run(scenario, func(t *testing.T) {
			x := preparationExchange(func(_ context.Context, got control.Ticket) (control.Result, error) {
				wire, _ := json.Marshal(got)
				if string(wire) != string(raw) {
					t.Fatal("not the deterministic cancellation")
				}
				if scenario == "transport" {
					return control.Result{}, errors.New("lost response")
				}
				if scenario == "unknown" {
					return control.NewErrorResult(got.OperationID, got.RequestID, got.Type, control.ErrorBody{Code: "NEW-9999", Message: "alpha diagnostic"}, time.Now())
				}
				p := control.CancelPassportPreparationResult{PreparationOperationID: original.OperationID, PreparationRequestID: original.RequestID, State: "canceled"}
				if scenario == "wrong-original" {
					p.PreparationRequestID = uuid.NewString()
				}
				r, err := control.NewSuccessResult(got.OperationID, got.RequestID, got.Type, p, time.Now())
				if scenario == "wrong-outer" {
					r.RequestID = uuid.NewString()
				}
				if scenario == "acquired" {
					p.State = "acquired"
					r.Result, _ = json.Marshal(p)
				}
				return r, err
			})
			err := controlclient.CancelPassportPreparation(t.Context(), x, original)
			if (err == nil) != (scenario == "canceled") {
				t.Fatalf("receipt: %v", err)
			}
			if scenario == "unknown" && !strings.Contains(err.Error(), "alpha diagnostic") {
				t.Fatal("unknown diagnostic hidden")
			}
		})
	}
}
