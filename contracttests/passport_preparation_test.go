package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"github.com/google/uuid"
)

func TestCatalogFaultClassificationPreservesExactPair(t *testing.T) {
	for _, spec := range errcat.All() {
		fault := errcat.Of(spec.Code, spec.Key, nil, nil)
		entry := errmap.Classify(fmt.Errorf("guard: %w", fault))
		if entry.Code != spec.Code || entry.Key != spec.Key || entry.Hint != spec.Hint || entry.Severity != spec.Severity {
			t.Fatalf("catalog pair %s/%s changed to %+v", spec.Code, spec.Key, entry)
		}
	}
}

type preparationExchange func(context.Context, control.Ticket) (control.Result, error)

func (f preparationExchange) Exchange(ctx context.Context, t control.Ticket) (control.Result, error) {
	return f(ctx, t)
}

func TestControlPassportPreparationContract(t *testing.T) {
	payload := control.PreparePassportReplacementPayload{RepoID: uuid.NewString(), Path: "projekty/Łódź.dwg", ObservedLockID: "opaquelocktoken:" + uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "migrate"}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, uuid.NewString(), payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(ticket)
	parsed, err := control.ParseTicket(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"prepared", "not-a-lock", "wrong-path", "wrong-token", "wrong-operation", "denied", "uncertain", "unknown", "transport"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			transport := preparationExchange(func(_ context.Context, got control.Ticket) (control.Result, error) {
				calls++
				sent, _ := json.Marshal(got)
				if string(sent) != string(wire) {
					t.Fatal("request identity changed")
				}
				if scenario == "transport" {
					return control.Result{}, errors.New("lost SSH reply")
				}
				if scenario == "denied" || scenario == "uncertain" || scenario == "unknown" {
					key := errcat.KeyPassportDenied
					if scenario == "uncertain" {
						key = errcat.KeyPassportUncertain
					}
					spec, _ := errcat.ByKey(key)
					code, message := string(spec.Code), string(spec.Key)
					if scenario == "unknown" {
						code, message = "NEW-9999", "new server diagnostic"
					}
					return control.NewErrorResult(got.OperationID, got.RequestID, got.Type, control.ErrorBody{Code: code, Message: message}, time.Now())
				}
				receipt := control.PreparePassportReplacementResult{RepoID: payload.RepoID, Path: payload.Path, ObservedLockID: payload.ObservedLockID, State: "prepared"}
				if scenario == "wrong-path" {
					receipt.Path = "another.dwg"
				}
				if scenario == "wrong-token" {
					receipt.ObservedLockID = "newer-token"
				}
				r, e := control.NewSuccessResult(got.OperationID, got.RequestID, got.Type, receipt, time.Now())
				if scenario == "wrong-operation" {
					r.OperationID = uuid.NewString()
				}
				if scenario == "not-a-lock" {
					receipt.State = "acquired"
					r.Result, _ = json.Marshal(receipt)
				}
				return r, e
			})
			err := controlclient.PreparePassportReplacement(t.Context(), transport, parsed)
			if (err == nil) != (scenario == "prepared") {
				t.Fatalf("error=%v", err)
			}
			if calls != 1 {
				t.Fatalf("automatic mutation retry: %d", calls)
			}
			if scenario == "denied" || scenario == "uncertain" {
				var fault errcat.Fault
				if !errors.As(err, &fault) {
					t.Fatalf("not a catalog fault: %v", err)
				}
				entry := errmap.Classify(fmt.Errorf("publish guard: %w", err))
				if entry.Code != fault.Code || entry.Key != fault.Key {
					t.Fatalf("catalog identity lost: %+v", entry)
				}
			}
			if scenario == "unknown" && !strings.Contains(err.Error(), "new server diagnostic") {
				t.Fatal("alpha diagnostic hidden")
			}
		})
	}
}

func TestControlPassportPreparationRejectsUntrustedFields(t *testing.T) {
	base := control.PreparePassportReplacementPayload{RepoID: uuid.NewString(), Path: "file.txt", ObservedLockID: "opaque-token", PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "renew"}
	for _, scenario := range []string{"path", "token", "mode", "passport", "realm", "holder"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			switch scenario {
			case "path":
				p["path"] = "../outside"
			case "token":
				p["observed_lock_id"] = "bad\nvalue"
			case "mode":
				p["mode"] = "force"
			case "passport":
				p["passport_id"] = "not-a-uuid"
			case "realm":
				p["realm_id"] = uuid.NewString()
			case "holder":
				p["holder_client_id"] = uuid.NewString()
			}
			if _, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketPreparePassportReplacement, uuid.NewString(), p, time.Now()); err == nil {
				t.Fatal("unsafe payload accepted")
			}
		})
	}
}
