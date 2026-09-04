package servertool

import (
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
)

const testClientID = "22222222-2222-4222-8222-222222222222"

func reservationTestRepository(repoID, state string) clientview.Repository {
	return clientview.Repository{
		RepoID:      repoID,
		DisplayName: "docs",
		URL:         "svn+ssh://" + url.User("_filees-client").String() + "@filees.test/" + repoID,
		Access:      "rw",
		State:       state,
	}
}

func writeTestClientView(t *testing.T, serviceWC string, repos []clientview.Repository) {
	t.Helper()
	path := filepath.Join(serviceWC, "clients", testClientID, "view.json")
	view := clientview.View{
		Schema: clientview.Schema, ServerDisplayName: "Serwer testowy", ClientID: testClientID, RealmID: "33333333-3333-4333-8333-333333333333",
		Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Repositories: repos,
	}
	if _, err := clientview.StoreIfNewer(path, view); err != nil {
		t.Fatalf("write client view fixture: %v", err)
	}
}

func TestAuthorizeReservationRequestAllowsActiveRepoInClientView(t *testing.T) {
	serviceWC := t.TempDir()
	writeTestClientView(t, serviceWC, []clientview.Repository{reservationTestRepository(repoID, "active")})
	if err := authorizeReservationRequest(serviceWC, testClientID, repoID); err != nil {
		t.Fatalf("expected authorization to succeed: %v", err)
	}
}

func TestAuthorizeReservationRequestDeniesRepoNotInView(t *testing.T) {
	serviceWC := t.TempDir()
	writeTestClientView(t, serviceWC, []clientview.Repository{reservationTestRepository("44444444-4444-4444-8444-444444444444", "active")})
	err := authorizeReservationRequest(serviceWC, testClientID, repoID)
	if !errors.Is(err, errReservationAccessDenied) {
		t.Fatalf("expected access denied, got %v", err)
	}
}

func TestAuthorizeReservationRequestDeniesInactiveRepo(t *testing.T) {
	serviceWC := t.TempDir()
	writeTestClientView(t, serviceWC, []clientview.Repository{reservationTestRepository(repoID, "disabled")})
	err := authorizeReservationRequest(serviceWC, testClientID, repoID)
	if !errors.Is(err, errReservationAccessDenied) {
		t.Fatalf("expected access denied for a non-active repository, got %v", err)
	}
}

func TestAuthorizeReservationRequestDeniesUnknownClient(t *testing.T) {
	serviceWC := t.TempDir()
	err := authorizeReservationRequest(serviceWC, testClientID, repoID)
	if !errors.Is(err, errReservationAccessDenied) {
		t.Fatalf("expected access denied for a client with no view on disk, got %v", err)
	}
}
