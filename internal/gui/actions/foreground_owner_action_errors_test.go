package actions_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/realmbranding"
)

type failingPublicShares struct{ err error }

func (f failingPublicShares) ListPublicShares(context.Context, string, string) ([]actions.PublicShareSummary, error) {
	return nil, f.err
}
func (failingPublicShares) CreatePublicShare(context.Context, string, actions.PublicShareDeclaration) error {
	return nil
}
func (failingPublicShares) UpdatePublicShare(context.Context, string, string, actions.PublicShareDeclaration) error {
	return nil
}
func (failingPublicShares) RevokePublicShare(context.Context, string, string, string) error {
	return nil
}
func (failingPublicShares) DeletePublicShare(context.Context, string, string, string) error {
	return nil
}

type failingBranding struct{ err error }

func (f failingBranding) PublicBranding(context.Context, string) (realmbranding.Branding, error) {
	return realmbranding.Branding{}, f.err
}
func (failingBranding) SetPublicBranding(context.Context, string, realmbranding.Branding) (realmbranding.Branding, error) {
	return realmbranding.Branding{}, nil
}

func waitForInfoRequest(t *testing.T, fake *platformtest.Fake) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(fake.Snapshot().InfoRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestControllerPublicShareListFailureUsesForegroundModal(t *testing.T) {
	platformFake := &platformtest.Fake{SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogPublicShares, ServerID: "office", RepoID: "repo-1"}, nil
	}}
	view := lifecycleView(contract.CapRepoPublicShareList, contract.CapRepoPublicShareCreate, contract.CapRepoPublicShareUpdate, contract.CapRepoPublicShareRevoke, contract.CapRepoPublicShareDelete)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, PublicShareBrowser: platformFake, FolderPicker: platformFake, Prompter: platformFake, PublicShares: failingPublicShares{err: errors.New("public share backend failed")}, Notifier: platformFake})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	waitForInfoRequest(t, platformFake)
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || !strings.Contains(snapshot.InfoRequests[0].Text, "backend failed") {
		t.Fatalf("foreground error = %+v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 {
		t.Fatalf("notification history = %+v", snapshot.Notifications)
	}
}

func TestControllerBrandingReadFailureUsesForegroundModal(t *testing.T) {
	platformFake := &platformtest.Fake{SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogRealmBranding, ServerID: "office"}, nil
	}}
	view := lifecycleView(contract.CapRealmPublicBrandingGet, contract.CapRealmPublicBrandingSet)
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(view), SettingsBrowser: platformFake, Picker: platformFake, Prompter: platformFake, RealmBranding: failingBranding{err: errors.New("branding backend failed")}, Notifier: platformFake})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	waitForInfoRequest(t, platformFake)
	snapshot := platformFake.Snapshot()
	if len(snapshot.InfoRequests) != 1 || !strings.Contains(snapshot.InfoRequests[0].Text, "backend failed") {
		t.Fatalf("foreground error = %+v", snapshot.InfoRequests)
	}
	if len(snapshot.Notifications) != 1 {
		t.Fatalf("notification history = %+v", snapshot.Notifications)
	}
}
