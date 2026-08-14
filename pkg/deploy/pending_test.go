package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingInvitationActivationsListsOnlyAcceptedIncompleteAttempts(t *testing.T) {
	root := t.TempDir()
	acceptedRoot := filepath.Join(root, "office")
	if err := os.MkdirAll(acceptedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	accepted := newInvitationPassport(ServerProfile{ID: "office", Address: "filees.example.net:22", KnownHostsPath: filepath.Join(acceptedRoot, "known_hosts")}, "0123456789abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ-_")
	accepted.State = passportAccepted
	accepted.InvitationToken = ""
	accepted.WorkerPublicKey = testBarePublicKey(t)
	accepted.RemotePort = 42000
	if err := writeJSONAtomic(filepath.Join(acceptedRoot, "onboard.json"), accepted, 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := PendingInvitationActivations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != "office" || profiles[0].Address != "filees.example.net:22" {
		t.Fatalf("profiles=%+v", profiles)
	}
	accepted.InvitationTokenHash = ""
	if err := writeJSONAtomic(filepath.Join(acceptedRoot, "onboard.json"), accepted, 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err = PendingInvitationActivations(root)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("legacy accepted profiles=%+v err=%v", profiles, err)
	}

	if err := os.WriteFile(filepath.Join(acceptedRoot, "client-profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err = PendingInvitationActivations(root)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("completed profiles=%+v err=%v", profiles, err)
	}
}

func TestPendingInvitationActivationsRejectsRelativeRoot(t *testing.T) {
	if _, err := PendingInvitationActivations("relative"); err == nil {
		t.Fatal("relative state root accepted")
	}
}
