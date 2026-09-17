package repoworker

import (
	"context"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

func TestDemoWorkerRefusesEverythingAcrossRealms(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	grants := &fakeRealmGrantAuthority{}
	minter := &fakeMobilePairingMinter{}
	loader := &refusingDumpLoader{}
	worker := &Worker{Store: store, Grants: grants, MobilePairing: minter, DumpLoader: loader, Demo: true}
	ticket := func(typ control.TicketType, payload any) control.Ticket {
		t.Helper()
		value, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, session.ClientID, payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	refused := func(what string, result control.Result, err error) {
		t.Helper()
		if err != nil || result.Status != control.ResultError || result.Error == nil || result.Error.Code != DemoPolicyCode {
			t.Fatalf("%s: result=%+v err=%v, want %s", what, result, err, DemoPolicyCode)
		}
	}

	result, err := worker.Handle(context.Background(), session, ticket(control.TicketGrantAccess, control.GrantAccessPayload{RepoID: uuid.NewString(), RecipientRealmID: uuid.NewString(), Access: "r"}))
	refused("grant", result, err)
	result, err = worker.Handle(context.Background(), session, ticket(control.TicketSetRealmVisibility, control.SetRealmDirectoryVisibilityPayload{Visibility: "listed"}))
	refused("listed", result, err)
	result, err = worker.Handle(context.Background(), session, mobilePairingTicket(t, session.ClientID))
	refused("mobile pairing", result, err)
	result, err = worker.Handle(context.Background(), session, ticket(control.TicketLoadRepositoryDump, control.LoadRepositoryDumpPayload{RepoID: uuid.NewString()}))
	refused("load dump", result, err)
	if loader.calls != 0 {
		t.Fatal("demo dump load reached svnadmin load, which bypasses pre-commit")
	}
	if grants.grantCalls != 0 || grants.visibility != "" || minter.calls != 0 {
		t.Fatalf("demo refusal reached an authority: grants=%+v minter calls=%d", grants, minter.calls)
	}

	result, err = worker.Handle(context.Background(), session, ticket(control.TicketListGrantRecipients, control.ListGrantRecipientsPayload{RepoID: uuid.NewString()}))
	if err != nil || result.Status != control.ResultOK {
		t.Fatalf("recipients: result=%+v err=%v", result, err)
	}
	var recipients control.ListGrantRecipientsResult
	if err := control.DecodeResultPayload(result.Result, &recipients); err != nil || len(recipients.Recipients) != 0 {
		t.Fatalf("demo directory is not empty: %+v err=%v", recipients, err)
	}

	result, err = worker.Handle(context.Background(), session, ticket(control.TicketSetRealmVisibility, control.SetRealmDirectoryVisibilityPayload{Visibility: "hidden"}))
	if err != nil || result.Status != control.ResultOK || grants.visibility != "hidden" {
		t.Fatalf("hiding a realm must stay possible: result=%+v err=%v visibility=%q", result, err, grants.visibility)
	}
}

type refusingDumpLoader struct{ calls int }

func (l *refusingDumpLoader) Load(context.Context, string, string, string, bool, *int) (LoadedDump, error) {
	l.calls++
	return LoadedDump{}, nil
}
