package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

type failingActivationService struct{ err error }

func (s failingActivationService) Begin(context.Context, contract.ActivationBeginPayload) (contract.ActivationCommandResult, error) {
	return contract.ActivationCommandResult{}, s.err
}
func (s failingActivationService) Finish(context.Context, contract.ActivationFinishPayload) (contract.ActivationCommandResult, error) {
	return contract.ActivationCommandResult{}, s.err
}
func (s failingActivationService) Pending(context.Context, contract.ActivationPendingPayload) (contract.ActivationPendingResult, error) {
	return contract.ActivationPendingResult{}, s.err
}
func (s failingActivationService) Resume(context.Context, contract.ActivationResumePayload) (contract.ActivationCommandResult, error) {
	return contract.ActivationCommandResult{}, s.err
}

// Activation is the worst place in the product to answer with a sentence that
// has no cause. It is the first thing a person does, so there is no repository
// to inspect, no history to compare against and no second screen to check -
// whatever the dialog says is all they get.
//
// The daemon knew the whole time: unreachable address, spent invitation, closed
// port. It wrote that to its own log and handed the interface nil, which is the
// defect RECOVERY-1001 carried until r696, in a flow with even less to fall
// back on.
func TestActivationBeginFailureCarriesItsCause(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	server.SetActivationService(failingActivationService{err: errors.New("dial tcp: connection refused")})

	payload, err := json.Marshal(contract.ActivationBeginPayload{
		ServerID: "cloud", ServerAddress: "cloud.example.net:2222", Invitation: "FILEES-INVITE-TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := server.handleActivationBegin(contract.Request{RequestID: "req-1", Payload: payload})

	if resp.Error == nil {
		t.Fatal("a failed activation must report an error")
	}
	if resp.Error.Code != "ACTIVATION-1001" {
		t.Fatalf("code = %q", resp.Error.Code)
	}
	detail := resp.Error.Details["detail"]
	if detail == "" {
		t.Fatal("the cause the daemon already has must reach the person who can act on it")
	}
	if !strings.Contains(detail, "connection refused") {
		t.Fatalf("the detail must be the real reason, not a restatement of the code: %q", detail)
	}
}

// The two steps after it fail the same way and had the same gap. A person who
// gets past the invitation and stalls on the OTP is no better placed to guess.
func TestActivationFinishAndResumeCarryTheirCause(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	server.SetActivationService(failingActivationService{err: errors.New("otp already used")})

	finishPayload, err := json.Marshal(contract.ActivationFinishPayload{ServerID: "cloud", OTP: contract.Secret("123456")})
	if err != nil {
		t.Fatal(err)
	}
	finish := server.handleActivationFinish(contract.Request{RequestID: "req-2", Payload: finishPayload})
	if finish.Error == nil || finish.Error.Details["detail"] == "" {
		t.Fatalf("activation finish discards its cause: %+v", finish.Error)
	}

	resumePayload, err := json.Marshal(contract.ActivationResumePayload{ServerID: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	resume := server.handleActivationResume(contract.Request{RequestID: "req-3", Payload: resumePayload})
	if resume.Error == nil || resume.Error.Details["detail"] == "" {
		t.Fatalf("activation resume discards its cause: %+v", resume.Error)
	}
}
