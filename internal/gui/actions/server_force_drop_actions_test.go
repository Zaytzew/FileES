package actions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type fakeServerDetacher struct {
	detachErr error
	dropped   chan string
}

func (fake *fakeServerDetacher) DetachServer(context.Context, string) error { return fake.detachErr }

func (fake *fakeServerDetacher) ForceDropServer(_ context.Context, serverID string) error {
	fake.dropped <- serverID
	return nil
}

func waitSnapshot(fake *platformtest.Fake, done func(platformtest.Snapshot) bool) platformtest.Snapshot {
	deadline := time.Now().Add(2 * time.Second)
	snapshot := fake.Snapshot()
	for !done(snapshot) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		snapshot = fake.Snapshot()
	}
	return snapshot
}

func TestServerDetachFailureOffersForceDropAndRunsItOnlyWhenConfirmed(t *testing.T) {
	unreachable := errors.New("repository control SSH handshake: connection reset by peer")
	for _, accept := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "declined"}[accept], func(t *testing.T) {
			detacher := &fakeServerDetacher{detachErr: unreachable, dropped: make(chan string, 1)}
			fake := &platformtest.Fake{ConfirmFunc: func(_ context.Context, request platform.ConfirmRequest) (bool, error) {
				return request.PresentationKey != "dialog.forceDropServer" || accept, nil
			}}
			intents, cancel := setup(actions.Config{
				ViewModel: viewCopy(lifecycleView(contract.CapServerDetach, contract.CapServerForceDrop)),
				Prompter:  fake, Notifier: fake, ServerDetacher: detacher,
			})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentDetachServer, ServerID: "office"})
			snapshot := waitSnapshot(fake, func(s platformtest.Snapshot) bool { return len(s.ConfirmRequests) == 2 })
			if len(snapshot.ConfirmRequests) != 2 || snapshot.ConfirmRequests[1].PresentationKey != "dialog.forceDropServer" ||
				snapshot.ConfirmRequests[1].PresentationArgs["reason"] == "" {
				t.Fatalf("force drop not offered with its reason: %+v", snapshot.ConfirmRequests)
			}
			if !accept {
				select {
				case id := <-detacher.dropped:
					t.Fatalf("declined force drop still removed %s", id)
				case <-time.After(50 * time.Millisecond):
				}
				if len(fake.Snapshot().InfoRequests) != 0 {
					t.Fatal("declining was reported as a failure")
				}
				return
			}
			if id := awaitCh(t, detacher.dropped, "force drop"); id != "office" {
				t.Fatalf("dropped %q", id)
			}
			snapshot = waitSnapshot(fake, func(s platformtest.Snapshot) bool { return len(s.Notifications) > 0 })
			if len(snapshot.Notifications) == 0 || snapshot.Notifications[0].Title != "Serwer usunięty z tego komputera" {
				t.Fatalf("notifications=%+v", snapshot.Notifications)
			}
		})
	}
}

func TestServerDetachFailureWithoutForceDropCapabilityReportsTheFailure(t *testing.T) {
	detacher := &fakeServerDetacher{detachErr: errors.New("offline"), dropped: make(chan string, 1)}
	fake := &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil }}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(lifecycleView(contract.CapServerDetach)),
		Prompter:  fake, Notifier: fake, ServerDetacher: detacher,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentDetachServer, ServerID: "office"})
	snapshot := waitSnapshot(fake, func(s platformtest.Snapshot) bool { return len(s.InfoRequests) > 0 })
	if len(snapshot.InfoRequests) != 1 || len(snapshot.ConfirmRequests) != 1 {
		t.Fatalf("an older daemon must keep the plain failure: confirms=%+v info=%+v", snapshot.ConfirmRequests, snapshot.InfoRequests)
	}
	select {
	case <-detacher.dropped:
		t.Fatal("force drop ran without the capability")
	default:
	}
}
