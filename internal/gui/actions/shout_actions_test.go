package actions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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

func (f *fakeShouts) BeginDraft(ctx context.Context, _ string) (context.Context, func(), error) {
	return ctx, func() {}, nil
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

type draftPublisher struct {
	acquired  chan struct{}
	released  chan struct{}
	beginErr  error
	published chan bool
}
type draftTestKey struct{}

func (p *draftPublisher) BeginDraft(ctx context.Context, _ string) (context.Context, func(), error) {
	if p.beginErr != nil {
		return nil, nil, p.beginErr
	}
	close(p.acquired)
	var once sync.Once
	return context.WithValue(ctx, draftTestKey{}, true), func() { once.Do(func() { close(p.released) }) }, nil
}
func (p *draftPublisher) Publish(ctx context.Context, _, _ string) (int64, error) {
	protected, _ := ctx.Value(draftTestKey{}).(bool)
	select {
	case <-p.released:
		protected = false
	default:
	}
	p.published <- protected
	return 7, nil
}
func TestShoutEditorOwnsPauseThroughSendAndReleasesOnEveryExit(t *testing.T) {
	for _, mode := range []string{"send", "cancel", "prompt-error", "gui-close", "begin-error"} {
		t.Run(mode, func(t *testing.T) {
			p := &draftPublisher{acquired: make(chan struct{}), released: make(chan struct{}), published: make(chan bool, 1)}
			if mode == "begin-error" {
				p.beginErr = errors.New("no lease")
			}
			opened := make(chan struct{})
			fake := &platformtest.Fake{PromptTextFunc: func(ctx context.Context, _ platform.PromptTextRequest) (platform.PromptTextResult, error) {
				select {
				case <-p.acquired:
				default:
					t.Error("editor opened before pause")
				}
				close(opened)
				switch mode {
				case "cancel":
					return platform.PromptTextResult{Cancelled: true}, nil
				case "prompt-error":
					return platform.PromptTextResult{}, errors.New("closed")
				case "gui-close":
					<-ctx.Done()
					return platform.PromptTextResult{}, ctx.Err()
				}
				return platform.PromptTextResult{Value: "ready"}, nil
			}}
			intents, cancel := setup(actions.Config{ViewModel: publishView, Prompter: fake, Notifier: fake, Shouts: p})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentPublish, RepoID: "docs"})
			if mode == "begin-error" {
				deadline := time.After(time.Second)
				for len(fake.Snapshot().InfoRequests) == 0 {
					select {
					case <-opened:
						t.Fatal("unguarded editor opened")
					case <-deadline:
						t.Fatal("missing error")
					case <-time.After(time.Millisecond):
					}
				}
				return
			}
			select {
			case <-opened:
			case <-time.After(time.Second):
				t.Fatal("editor did not open")
			}
			if mode == "gui-close" {
				cancel()
			}
			select {
			case <-p.released:
			case <-time.After(time.Second):
				t.Fatal("pause leaked")
			}
			if mode == "send" {
				if !awaitCh(t, p.published, "publish lease") {
					t.Fatal("send lost pause/token")
				}
			} else {
				select {
				case <-p.published:
					t.Fatal("cancel published")
				default:
				}
			}
		})
	}
}
