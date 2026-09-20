package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	contract "filees/pkg/contract/v1"
	"filees/pkg/controlclient"
	"fmt"
	"net"
	"testing"
)

func TestRemoteActionFailurePreservesCauseAndSeparatesConnectivity(t *testing.T) {
	for _, tc := range []struct {
		err error
		key string
	}{
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, "server.action_unreachable"},
		{context.DeadlineExceeded, "server.action_unreachable"},
		{fmt.Errorf("auth: %w", controlclient.ErrIdentityRefused), "realm.remove_begin_failed"},
		{errors.New("syntax error: NUL byte unexpected"), "realm.remove_begin_failed"},
	} {
		s := New(t.TempDir())
		s.RegisterActivation(contract.ActivationStatus{ServerID: "lab", RealmID: "realm"})
		s.SetRealmRemovalService(failingRecoveryService{err: tc.err})
		raw, _ := json.Marshal(contract.RealmRemoveBeginPayload{ServerID: "lab", NotificationEmail: "a@example.com", RecoveryDirectory: t.TempDir()})
		r := s.handleRealmRemoveBegin(contract.Request{RequestID: "test", Payload: raw})
		if r.Error == nil || r.Error.MessageKey != tc.key || r.Error.Details["detail"] != tc.err.Error() {
			t.Fatalf("%+v", r.Error)
		}
	}
}
