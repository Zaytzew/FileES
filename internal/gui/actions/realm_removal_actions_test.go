package actions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
)

type fakeRealmRemover struct {
	begin   chan actions.RealmRemovalBeginRequest
	confirm chan string
}

func (fake *fakeRealmRemover) BeginRealmRemoval(_ context.Context, request actions.RealmRemovalBeginRequest) (actions.RealmRemovalBeginResult, error) {
	fake.begin <- request
	return actions.RealmRemovalBeginResult{
		OperationID: "operation", RecoveryKitPath: "/tmp/recovery/filees.fkr",
		ActiveClientCount: 3, OwnedRepositoryCount: 2, ForeignGrantCount: 1,
	}, nil
}

func (fake *fakeRealmRemover) ConfirmRealmRemoval(_ context.Context, serverID, operationID string, otp []byte, kitPath string) (actions.RealmRemovalConfirmResult, error) {
	fake.confirm <- strings.Join([]string{serverID, operationID, string(otp), kitPath}, "|")
	return actions.RealmRemovalConfirmResult{RecoveryKitPath: kitPath, ArchiveCount: 2, DownloadUntil: "2026-08-29", AdminGraceUntil: "2026-09-08", ErasureRequested: true, ErasureMaxDays: 90}, nil
}

func TestSettingsRealmRemovalRequiresRetentionConsentAndCarriesOptionalErasureIntent(t *testing.T) {
	remover := &fakeRealmRemover{begin: make(chan actions.RealmRemovalBeginRequest, 1), confirm: make(chan string, 1)}
	fake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogRemoveRealm, ServerID: "office"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
		ConsentFunc: func(context.Context, platform.ConsentRequest) (platform.ConsentResult, error) {
			return platform.ConsentResult{Required: true, Optional: true}, nil
		},
		PromptTextFunc: func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
			if request.Secret {
				return platform.PromptTextResult{Value: "ABCDEFGH234567"}, nil
			}
			return platform.PromptTextResult{Value: "user@example.net"}, nil
		},
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: "/tmp/recovery"}, nil
		},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(lifecycleView()), SettingsBrowser: fake, Prompter: fake,
		ConsentPrompter: fake, FolderPicker: fake, RealmRemover: remover, Notifier: fake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	begin := awaitCh(t, remover.begin, "realm removal begin")
	if begin.ServerID != "office" || begin.NotificationEmail != "user@example.net" || begin.RecoveryDirectory != "/tmp/recovery" || !begin.ErasureRequested {
		t.Fatalf("begin=%+v", begin)
	}
	if got := awaitCh(t, remover.confirm, "realm removal confirm"); got != "office|operation|ABCDEFGH234567|/tmp/recovery/filees.fkr" {
		t.Fatalf("confirm=%q", got)
	}
	deadline := time.Now().Add(time.Second)
	snapshot := fake.Snapshot()
	for len(snapshot.InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		snapshot = fake.Snapshot()
	}
	if len(snapshot.ConsentRequests) != 1 || len(snapshot.InfoRequests) != 1 || !strings.Contains(snapshot.InfoRequests[0].Text, "90 dni") {
		t.Fatalf("consent/info=%+v/%+v", snapshot.ConsentRequests, snapshot.InfoRequests)
	}
}

func TestSettingsRealmRemovalStopsWhenMandatoryRetentionConsentIsUnchecked(t *testing.T) {
	remover := &fakeRealmRemover{begin: make(chan actions.RealmRemovalBeginRequest, 1), confirm: make(chan string, 1)}
	fake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogRemoveRealm, ServerID: "office"}, nil
		},
		ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) { return true, nil },
		ConsentFunc: func(context.Context, platform.ConsentRequest) (platform.ConsentResult, error) {
			return platform.ConsentResult{Optional: true}, nil
		},
	}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView()), SettingsBrowser: fake, Prompter: fake, ConsentPrompter: fake, FolderPicker: fake, RealmRemover: remover})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case call := <-remover.begin:
		t.Fatalf("realm removal started without mandatory consent: %+v", call)
	case <-time.After(100 * time.Millisecond):
	}
}
