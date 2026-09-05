package contracttests

import (
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func TestBrokerNameCrossesExistingIPCWithoutChangingInvitationIdentity(t *testing.T) {
	server, _, sock := startEventServer(t)
	original := contract.ActivationStatus{ServerID: "spot", DisplayName: "spot", Address: "spot.example.net:2223", SSHPort: 2223, ClientID: "same-client", RealmID: "same-realm"}
	server.RegisterActivation(original)
	server.SetBrokerServerDisplayName("spot", "40rs:filees")
	server.RegisterActivation(original) // queued old view
	status, err := ipcclient.New(sock, "gui").SystemStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Activations) != 1 {
		t.Fatalf("activations=%+v", status.Activations)
	}
	original.DisplayName = "40rs:filees"
	if status.Activations[0] != original {
		t.Fatalf("wire changed identity: %+v", status.Activations[0])
	}
}
