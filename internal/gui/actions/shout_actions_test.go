package actions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type fakeShouts struct {
	rev int64
	err error
	ch  chan publishCall
}

type publishCall struct{ repoID, comment string }

func (f *fakeShouts) Publish(_ context.Context, repoID, comment string) (int64, error) {
	if f.ch != nil {
		f.ch <- publishCall{repoID: repoID, comment: comment}
	}
	return f.rev, f.err
}

type fakeShoutError struct{ key string }

func (e fakeShoutError) Error() string { return "wire " + e.key }
func (e fakeShoutError) PresentationError() (string, string, string, string) {
	return "SHOUT-1001", "ERROR", "REQUIRE_ACTION", e.key
}
func (e fakeShoutError) PresentationDetails() map[string]string { return nil }

func publishView() app.ViewModel {
	return app.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapRepoPublish: true},
		Repos:        []app.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty", Access: contract.AccessReadWrite}},
	}
}

func TestControllerPublishShowsPolishEmptyState(t *testing.T) {
	publisher := &fakeShouts{err: fakeShoutError{key: "shout.nothing_to_publish"}, ch: make(chan publishCall, 1)}
	fake := &platformtest.Fake{
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "materiały"}, nil
		},
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return publishView() }, Prompter: fake, Notifier: fake, Shouts: publisher})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPublish, RepoID: "docs"})
	if got := awaitCh(t, publisher.ch, "publish"); got.comment != "materiały" {
		t.Fatalf("publish=%#v", got)
	}
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := fake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Brak zmian do opublikowania" || !strings.Contains(snapshot.InfoRequests[0].Text, "zgodny z serwerem") || strings.Contains(snapshot.InfoRequests[0].Text, "nothing") {
		t.Fatalf("empty publish modal=%#v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 || snapshot.Notifications[0].Urgency == platform.UrgencyCritical {
		t.Fatalf("empty publish should not be a critical failure: %#v", snapshot.Notifications)
	}
}

func TestControllerPublishShowsModalOnSuccess(t *testing.T) {
	publisher := &fakeShouts{rev: 12, ch: make(chan publishCall, 1)}
	fake := &platformtest.Fake{
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "paka"}, nil
		},
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return publishView() }, Prompter: fake, Notifier: fake, Shouts: publisher})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPublish, RepoID: "docs"})
	awaitCh(t, publisher.ch, "publish")
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := fake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Wydanie opublikowane" || !strings.Contains(snapshot.InfoRequests[0].Text, "r12") {
		t.Fatalf("success modal=%#v", snapshot.InfoRequests)
	}
}

func TestControllerPublishRejectsInvalidCommentWithPolishModal(t *testing.T) {
	publisher := &fakeShouts{err: fakeShoutError{key: "shout.invalid_comment"}, ch: make(chan publishCall, 1)}
	fake := &platformtest.Fake{
		PromptTextFunc: func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
			return platform.PromptTextResult{Value: "   "}, nil
		},
	}
	intents, cancel := setup(actions.Config{ViewModel: func() app.ViewModel { return publishView() }, Prompter: fake, Notifier: fake, Shouts: publisher})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentPublish, RepoID: "docs"})
	awaitCh(t, publisher.ch, "publish")
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := fake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || snapshot.InfoRequests[0].Title != "Nieprawidłowy komentarz wydania" || strings.Contains(snapshot.InfoRequests[0].Text, "invalid_comment") {
		t.Fatalf("invalid comment modal=%#v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 || snapshot.Notifications[0].Urgency != platform.UrgencyCritical {
		t.Fatalf("notifications=%#v", snapshot.Notifications)
	}
}
