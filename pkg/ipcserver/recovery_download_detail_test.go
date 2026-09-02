package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
)

type failingRecoveryService struct{ err error }

func (s failingRecoveryService) Begin(context.Context, string, string, contract.RealmRemoveBeginPayload) (contract.RealmRemoveBeginResult, error) {
	return contract.RealmRemoveBeginResult{}, s.err
}
func (s failingRecoveryService) Confirm(context.Context, contract.RealmRemoveConfirmPayload) (contract.RealmRemoveConfirmResult, error) {
	return contract.RealmRemoveConfirmResult{}, s.err
}
func (s failingRecoveryService) List(context.Context) ([]contract.RecoveryStatus, error) {
	return nil, s.err
}
func (s failingRecoveryService) Download(context.Context, contract.RecoveryDownloadPayload) (contract.RecoveryDownloadResult, error) {
	return contract.RecoveryDownloadResult{}, s.err
}

// The cause used to be discarded: the envelope carried nil details and nothing
// was logged, so a failed archive download left no trace in the daemon log,
// the error journal or the lifecycle record. The user saw only the generic
// sentence and the failure could not be diagnosed at all.
func TestRecoveryDownloadFailureCarriesItsCause(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	server.SetRealmRemovalService(failingRecoveryService{err: errors.New("archive missing on server")})

	payload, err := json.Marshal(contract.RecoveryDownloadPayload{OperationID: "op-1", OutputRoot: `C:\out`})
	if err != nil {
		t.Fatal(err)
	}
	resp := server.handleRecoveryDownload(contract.Request{
		RequestID: "req-1",
		Payload:   payload,
	})

	if resp.Error == nil {
		t.Fatal("a failed download must produce an error envelope")
	}
	if resp.Error.Code != "RECOVERY-1001" {
		t.Fatalf("code = %q", resp.Error.Code)
	}
	detail := resp.Error.Details["detail"]
	if !strings.Contains(detail, "archive missing on server") {
		t.Fatalf("details = %v; the cause must survive to the caller", resp.Error.Details)
	}
}
