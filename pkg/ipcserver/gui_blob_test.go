package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"filees/pkg/guiblob"
	"github.com/google/uuid"
	"slices"
	"testing"
)

type guiBlobStub struct {
	state guiblob.State
	calls int
}

func (b *guiBlobStub) GUIBlob(_ context.Context, _ string, _ *guiblob.Write) (guiblob.State, error) {
	b.calls++
	return b.state, nil
}
func TestGUIBlobIPCAdmissionAndCapability(t *testing.T) {
	s := New("")
	realm := uuid.NewString()
	stub := &guiBlobStub{state: guiblob.State{RealmID: realm}}
	if slices.Contains(s.capabilities(), contract.CapGUIBlob) {
		t.Fatal("unwired capability")
	}
	s.SetGUIBlobService(stub)
	if !slices.Contains(s.capabilities(), contract.CapGUIBlob) {
		t.Fatal("missing capability")
	}
	req := lifecycleRequest(contract.CmdGUIBlobGet, contract.GUIBlobGetPayload{ServerID: "office"})
	if response := s.dispatch(req); response.Status == contract.StatusOK || stub.calls != 0 {
		t.Fatal("unactivated access", response)
	}
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", RealmID: realm, ClientRole: contract.ClientRoleReadOnly, CanCreateRepositories: true})
	if response := s.dispatch(req); response.Status == contract.StatusOK || stub.calls != 0 {
		t.Fatal("read-only access", response)
	}
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", RealmID: realm, CanCreateRepositories: true})
	if response := s.dispatch(req); response.Status != contract.StatusOK || stub.calls != 1 {
		t.Fatal(response)
	}
	stub.state.RealmID = uuid.NewString()
	if response := s.dispatch(req); response.Status == contract.StatusOK {
		t.Fatal("accepted another realm")
	}
}
