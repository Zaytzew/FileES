package main

import (
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
)

// A fresh activation must register the server the interface will actually be
// asked about. Measured on 2026-09-03: cloud granted the realm "pracownia" and
// repository creation, the projection on disk said so, and the settings window
// showed "alias nieustawiony" with no action but deactivation. Nothing
// corrected it either - clientview.Monitor emits on change, so a projection
// read once and unchanged never fires again and the blank registration stood
// until the daemon happened to restart.
func TestActivationRegistersTheRealmTheServerGranted(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "service-wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	// Decode rejects unknown fields, so this has to be a projection the client
	// would really accept - shaped exactly like the one cloud emitted.
	view := `{"schema":"filees.client-view/v2","server_display_name":"Cloud ATM Projekt","client_id":"399c0801-46d2-4190-bd70-15a9bf6cfa00",` +
		`"realm_id":"a72d443d-342b-4ed8-9412-925247dbd4c5","realm_alias":"pracownia",` +
		`"generation":2,"generated_at":"2026-09-03T10:18:00Z","client_role":"normal",` +
		`"capabilities":{"can_create_repositories":true},"repositories":[],"active_operations":[]}`
	if err := os.WriteFile(filepath.Join(wc, "view.json"), []byte(view), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := clientprofile.Profile{ServiceWC: wc, RelativeViewPath: "view.json"}

	got := withProjectedRealm(contract.ActivationStatus{ServerID: "atmprojekt:filees", ClientRole: "normal"}, profile)
	if got.RealmAlias != "pracownia" {
		t.Fatalf("the realm the server named must reach the interface: %+v", got)
	}
	if got.DisplayName != "Cloud ATM Projekt" {
		t.Fatalf("the server-owned display name must reach the interface: %+v", got)
	}
	if got.RealmID == "" {
		t.Fatalf("realm id = %q", got.RealmID)
	}
	if !got.CanCreateRepositories {
		t.Fatalf("the server granted repository creation and the interface must offer it: %+v", got)
	}
}

// An activation that succeeded must not be reported as failed because its
// projection could not be read a moment later. Missing it leaves exactly the
// previous behaviour - the realm arrives with the next projection - so this
// path can only improve on it, never worsen it.
func TestAnUnreadableProjectionLeavesTheActivationIntact(t *testing.T) {
	status := contract.ActivationStatus{ServerID: "atmprojekt:filees", ClientRole: "normal", ClientID: "abc"}
	got := withProjectedRealm(status, clientprofile.Profile{ServiceWC: t.TempDir(), RelativeViewPath: "view.json"})
	if got.ServerID != status.ServerID || got.ClientID != status.ClientID || got.ClientRole != "normal" {
		t.Fatalf("an unreadable projection must not disturb what activation established: %+v", got)
	}
	if got.RealmAlias != "" || got.CanCreateRepositories {
		t.Fatalf("nothing may be invented when the projection cannot be read: %+v", got)
	}
}

// The join case, which is how this was noticed. An invitation carrying a
// realm_id joins a realm that already has an alias, so the client must not ask
// the user to claim one. Without the projection at registration the interface
// saw no alias, concluded the realm had none yet, and prompted for a name that
// was already decided by the join.
func TestJoiningARealmDoesNotAskForAnAliasItAlreadyHas(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "service-wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	view := `{"schema":"filees.client-view/v2","server_display_name":"Cloud ATM Projekt","client_id":"399c0801-46d2-4190-bd70-15a9bf6cfa00","realm_id":"a72d443d-342b-4ed8-9412-925247dbd4c5",` +
		`"realm_alias":"pracownia","generation":2,"generated_at":"2026-09-03T10:18:00Z","client_role":"normal",` +
		`"capabilities":{"can_create_repositories":true},"repositories":[],"active_operations":[]}`
	if err := os.WriteFile(filepath.Join(wc, "view.json"), []byte(view), 0o600); err != nil {
		t.Fatal(err)
	}
	got := withProjectedRealm(contract.ActivationStatus{ServerID: "atmprojekt:filees", ClientRole: "normal"},
		clientprofile.Profile{ServiceWC: wc, RelativeViewPath: "view.json"})
	if got.RealmAlias == "" {
		t.Fatal("an empty alias is what makes the interface offer to claim one")
	}
}
