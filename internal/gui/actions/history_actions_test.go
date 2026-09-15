package actions_test

import (
	"context"
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

type recordingHistoryBrowser struct {
	mu     sync.Mutex
	opened []platform.HistoryOpenRequest
}

func (r *recordingHistoryBrowser) OpenHistory(_ context.Context, request platform.HistoryOpenRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, request)
	return nil
}

func (r *recordingHistoryBrowser) snapshot() []platform.HistoryOpenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]platform.HistoryOpenRequest(nil), r.opened...)
}

func historyView(capabilities ...string) app.ViewModel {
	view := lifecycleView(capabilities...)
	guest := app.RepoViewModel{
		ID: "guest-1", ServerID: "office", DisplayName: "Cudze", OwnerRealmID: "realm-2",
		Access: contract.AccessReadWrite, State: contract.StateActive, AttachmentPolicy: "optional",
	}
	shelf := app.RepoViewModel{
		ID: "shelf-1", ServerID: "office", DisplayName: "Półka", OwnerRealmID: "realm-1",
		Access: "r", State: contract.StateActive, AttachmentPolicy: "optional", Purpose: "upload_shelf",
	}
	view.Repos = append(view.Repos, guest, shelf)
	view.Servers[0].Repos = append(view.Servers[0].Repos, guest, shelf)
	return view
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerOffersHistoryOnlyForOwnedOrdinaryRepositories(t *testing.T) {
	browser := &recordingHistoryBrowser{}
	var mu sync.Mutex
	var offered []platform.SettingsFolder
	platformFake := &platformtest.Fake{SettingsFunc: func(_ context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if offered == nil && len(request.Servers) == 1 {
			offered = append(offered, request.Servers[0].Folders...)
			return platform.SettingsDialogResult{Action: platform.SettingsDialogBrowseHistory, ServerID: "office", RepoID: "repo-1"}, nil
		}
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(historyView(contract.CapRepoHistory)), SettingsBrowser: platformFake, HistoryBrowser: browser,
		FolderPicker: platformFake, Prompter: platformFake, Notifier: platformFake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	waitUntil(t, "history to open", func() bool { return len(browser.snapshot()) == 1 })
	if opened := browser.snapshot()[0]; opened != (platform.HistoryOpenRequest{ServerID: "office", RepoID: "repo-1"}) {
		t.Fatalf("opened = %+v", opened)
	}
	mu.Lock()
	defer mu.Unlock()
	allowed := map[string]bool{}
	for _, folder := range offered {
		allowed[folder.ID] = folder.CanBrowseHistory
	}
	if !allowed["repo-1"] || allowed["guest-1"] || allowed["shelf-1"] {
		t.Fatalf("history offered = %v", allowed)
	}
}

func TestControllerRefusesHistoryForGuestsAndDaemonsWithoutIt(t *testing.T) {
	for name, tc := range map[string]struct {
		view   app.ViewModel
		intent tray.Intent
	}{
		"guest repository":  {historyView(contract.CapRepoHistory), tray.Intent{Kind: tray.IntentBrowseHistory, ServerID: "office", RepoID: "guest-1"}},
		"upload shelf":      {historyView(contract.CapRepoHistory), tray.Intent{Kind: tray.IntentBrowseHistory, ServerID: "office", RepoID: "shelf-1"}},
		"no capability":     {historyView(), tray.Intent{Kind: tray.IntentBrowseHistory, ServerID: "office", RepoID: "repo-1"}},
		"tray without cap":  {historyView(), tray.Intent{Kind: tray.IntentBrowseHistory}},
		"other server repo": {historyView(contract.CapRepoHistory), tray.Intent{Kind: tray.IntentBrowseHistory, ServerID: "home", RepoID: "repo-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			browser := &recordingHistoryBrowser{}
			platformFake := &platformtest.Fake{}
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(tc.view), HistoryBrowser: browser, Notifier: platformFake})
			defer cancel()
			send(t, intents, tc.intent)
			waitUntil(t, "a notification", func() bool { return len(platformFake.Snapshot().Notifications) == 1 })
			if opened := browser.snapshot(); len(opened) != 0 {
				t.Fatalf("opened for %s: %+v", name, opened)
			}
		})
	}
}

func TestControllerTrayHistoryOpensTheChooser(t *testing.T) {
	browser := &recordingHistoryBrowser{}
	platformFake := &platformtest.Fake{}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(historyView(contract.CapRepoHistory)), HistoryBrowser: browser, Notifier: platformFake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentBrowseHistory})
	waitUntil(t, "history to open", func() bool { return len(browser.snapshot()) == 1 })
	if opened := browser.snapshot()[0]; opened != (platform.HistoryOpenRequest{}) {
		t.Fatalf("tray opened = %+v", opened)
	}
	if notes := platformFake.Snapshot().Notifications; len(notes) != 0 {
		t.Fatalf("an allowed opening notified: %+v", notes)
	}
}
