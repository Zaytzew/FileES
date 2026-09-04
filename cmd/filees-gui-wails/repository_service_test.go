package main

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/platform"
)

func TestRepositoryServiceProjectsOnlyFocusedFolderActions(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	request := platform.SettingsDialogRequest{
		Title: "Folder — Dokumenty", Text: "Działania dla folderu.", FocusRepoID: "docs",
		Servers: []platform.SettingsServer{{
			ID: "spot", Name: "spot", Address: "spot.example.net", Realm: "acme",
			Folders: []platform.SettingsFolder{{ID: "docs", Name: "Dokumenty", LocalPath: `E:\Dokumenty`, State: "aktywne", Access: "odczyt i zapis", Editing: "swobodna", CanManagePublicShares: true, CanDetach: true, CanDelete: true}, {ID: "other", Name: "Inny"}},
		}},
	}
	resultCh := make(chan platform.SettingsDialogResult, 1)
	go func() {
		result, _ := (repositorySettingsBrowserAdapter{service: service}).ShowSettings(context.Background(), request)
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("repository window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "actions" || snapshot.Context.RepoID != "docs" || len(snapshot.Actions) != 3 {
		t.Fatalf("repository snapshot = %+v", snapshot)
	}
	if got := service.ChooseAction(RepositoryChoice{Action: "public_shares", ServerID: "spot", RepoID: "other"}); got.Accepted || got.Code != "repository_context_changed" {
		t.Fatalf("foreign repository accepted: %+v", got)
	}
	if got := service.ChooseAction(RepositoryChoice{Action: "load_dump", ServerID: "spot", RepoID: "docs"}); got.Accepted || got.Code != "repository_action_unavailable" {
		t.Fatalf("unprojected action accepted: %+v", got)
	}
	if got := service.ChooseAction(RepositoryChoice{Action: "public_shares", ServerID: "spot", RepoID: "docs"}); !got.Accepted {
		t.Fatalf("public shares rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.SettingsDialogPublicShares || result.ServerID != "spot" || result.RepoID != "docs" {
			t.Fatalf("repository result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("repository browser did not return")
	}
}

func TestRepositoryServiceProjectsCurrentEditingPolicyAction(t *testing.T) {
	for _, test := range []struct {
		lockRequired bool
		wantLabel    string
	}{
		{lockRequired: false, wantLabel: "Włącz wypożyczanie plików"},
		{lockRequired: true, wantLabel: "Wyłącz wypożyczanie plików"},
	} {
		service := newRepositoryService()
		shown := make(chan struct{}, 1)
		service.attachPresentation(func() { shown <- struct{}{} }, func() {})
		request := platform.SettingsDialogRequest{FocusRepoID: "docs", Servers: []platform.SettingsServer{{
			ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", Name: "Dokumenty", CanSetEditingPolicy: true, LockRequired: test.lockRequired}},
		}}}
		resultCh := make(chan platform.SettingsDialogResult, 1)
		go func() {
			result, _ := (repositorySettingsBrowserAdapter{service: service}).ShowSettings(context.Background(), request)
			resultCh <- result
		}()
		select {
		case <-shown:
		case <-time.After(time.Second):
			t.Fatal("repository window was not shown")
		}
		snapshot := service.Snapshot()
		if len(snapshot.Actions) != 1 || snapshot.Actions[0].ID != "editing_policy" || snapshot.Actions[0].Label != test.wantLabel {
			t.Fatalf("editing policy projection = %+v", snapshot.Actions)
		}
		if accepted := service.ChooseAction(RepositoryChoice{Action: "editing_policy", ServerID: "spot", RepoID: "docs"}); !accepted.Accepted {
			t.Fatalf("editing policy rejected: %+v", accepted)
		}
		select {
		case result := <-resultCh:
			if result.Action != platform.SettingsDialogEditingPolicy || result.ServerID != "spot" || result.RepoID != "docs" {
				t.Fatalf("editing policy result = %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("repository browser did not return")
		}
	}
}

func TestRepositoryServiceProjectsUploadAndDumpActions(t *testing.T) {
	snapshot, ok := projectRepositorySettings(platform.SettingsDialogRequest{
		FocusRepoID: "docs",
		Servers:     []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", CanManageUploadChannels: true, CanLoadDump: true}}}},
	})
	if !ok || len(snapshot.Actions) != 2 || snapshot.Actions[0].ID != "upload_channels" || snapshot.Actions[1].ID != "load_dump" {
		t.Fatalf("upload/dump actions = ok %v snapshot %+v", ok, snapshot)
	}
}

func TestRepositoryServiceProjectsLifecycleRepairActions(t *testing.T) {
	snapshot, ok := projectRepositorySettings(platform.SettingsDialogRequest{
		FocusRepoID: "docs",
		Servers: []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{
			ID: "docs", CanRetryLifecycle: true, CanAbandonLifecycle: true,
		}}}},
	})
	if !ok || len(snapshot.Actions) != 2 || snapshot.Actions[0].ID != "retry_lifecycle" || snapshot.Actions[1].ID != "abandon_lifecycle" {
		t.Fatalf("repair actions = ok %v snapshot %+v", ok, snapshot)
	}
}

func TestRepositoryServiceValidatesPublicShareChoices(t *testing.T) {
	service := newRepositoryService()
	service.pendingShares = repositoryContextKey("spot", "docs")
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	request := platform.PublicShareDialogRequest{
		Title: "Udostępnienia", ServerID: "spot", RepoID: "docs", RepositoryName: "Dokumenty",
		Shares: []platform.PublicShareSummary{{ChannelID: "active", Address: "acme/docs", State: "aktywne"}, {ChannelID: "revoked", Address: "acme/old", State: "cofnięte"}},
	}
	resultCh := make(chan platform.PublicShareDialogResult, 1)
	go func() {
		result, _ := (repositoryPublicShareBrowserAdapter{service: service}).ShowPublicShares(context.Background(), request)
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("public shares window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "shares" || snapshot.Context.Name != "Dokumenty" || len(snapshot.Shares) != 2 || !snapshot.Shares[0].CanEdit || snapshot.Shares[1].CanEdit {
		t.Fatalf("public shares snapshot = %+v", snapshot)
	}
	if got := service.ChooseShare(RepositoryChoice{Action: "edit", ServerID: "spot", RepoID: "docs", ChannelID: "revoked"}); got.Accepted || got.Code != "public_share_action_unavailable" {
		t.Fatalf("revoked edit accepted: %+v", got)
	}
	if got := service.ChooseShare(RepositoryChoice{Action: "revoke", ServerID: "spot", RepoID: "docs", ChannelID: "active"}); !got.Accepted {
		t.Fatalf("active revoke rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.PublicShareDialogRevoke || result.ChannelID != "active" {
			t.Fatalf("public share result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("public share browser did not return")
	}
}

func TestRepositoryServiceRejectsStalePublicShareContinuation(t *testing.T) {
	service := newRepositoryService()
	service.pendingShares = repositoryContextKey("spot", "newer")
	result, err := (repositoryPublicShareBrowserAdapter{service: service}).ShowPublicShares(context.Background(), platform.PublicShareDialogRequest{ServerID: "spot", RepoID: "older"})
	if err != nil || result.Action != platform.PublicShareDialogClose || service.Snapshot().Revision != 0 {
		t.Fatalf("stale public shares = result %+v err %v snapshot %+v", result, err, service.Snapshot())
	}
}

func TestRepositoryServiceAllowsValidatedDirectPublicShareEntry(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	request := platform.PublicShareDialogRequest{
		Title: "Udostępnienia", ServerID: "spot", RepoID: "docs", RepositoryName: "Dokumenty",
		FocusChannelID: "active", DirectEntry: true,
		Shares: []platform.PublicShareSummary{{ChannelID: "active", Address: "acme/docs", State: "aktywne"}},
	}
	resultCh := make(chan platform.PublicShareDialogResult, 1)
	go func() {
		result, _ := (repositoryPublicShareBrowserAdapter{service: service}).ShowPublicShares(context.Background(), request)
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("direct public shares window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "shares" || snapshot.FocusChannelID != "active" || snapshot.Context.ServerID != "spot" || snapshot.Context.RepoID != "docs" {
		t.Fatalf("direct public shares snapshot = %+v", snapshot)
	}
	service.Cancel()
	select {
	case result := <-resultCh:
		if result.Action != platform.PublicShareDialogClose {
			t.Fatalf("direct public shares result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("direct public shares browser did not return")
	}
}

func TestRepositoryServiceBindsRealmGrantChoicesToCurrentFolder(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 2)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	settingsResult := make(chan platform.SettingsDialogResult, 1)
	go func() {
		result, _ := (repositorySettingsBrowserAdapter{service: service}).ShowSettings(context.Background(), platform.SettingsDialogRequest{
			FocusRepoID: "docs",
			Servers:     []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", Name: "Dokumenty", CanManageGrants: true}}}},
		})
		settingsResult <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("repository settings window was not shown")
	}
	if got := service.ChooseAction(RepositoryChoice{Action: "manage_grants", ServerID: "spot", RepoID: "docs"}); !got.Accepted {
		t.Fatalf("manage grants rejected: %+v", got)
	}
	select {
	case result := <-settingsResult:
		if result.Action != platform.SettingsDialogManageGrants {
			t.Fatalf("settings result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("repository settings browser did not return")
	}

	grantResult := make(chan platform.RealmGrantDialogResult, 1)
	go func() {
		result, _ := (repositoryRealmGrantBrowserAdapter{service: service}).ShowRealmGrants(context.Background(), platform.RealmGrantDialogRequest{
			Title: "Uprawnienia", Recipients: []platform.RealmGrantRecipient{
				{RealmID: "realm-read", Alias: "Czytelnicy", Access: "r", State: "active"},
				{RealmID: "realm-none", Alias: "Goście", State: "revoked"},
			},
		})
		grantResult <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("realm grants window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "grants" || len(snapshot.Grants) != 2 || snapshot.Grants[0].CanRead || !snapshot.Grants[0].CanWrite || !snapshot.Grants[0].CanRevoke {
		t.Fatalf("grant projection = %+v", snapshot)
	}
	if got := service.ChooseGrant(RepositoryChoice{Action: "grant_write", ServerID: "spot", RepoID: "other", RealmID: "realm-read"}); got.Accepted || got.Code != "repository_context_changed" {
		t.Fatalf("foreign grant context accepted: %+v", got)
	}
	if got := service.ChooseGrant(RepositoryChoice{Action: "revoke", ServerID: "spot", RepoID: "docs", RealmID: "realm-none"}); got.Accepted || got.Code != "realm_grant_action_unavailable" {
		t.Fatalf("inactive revoke accepted: %+v", got)
	}
	if got := service.ChooseGrant(RepositoryChoice{Action: "grant_write", ServerID: "spot", RepoID: "docs", RealmID: "realm-read"}); !got.Accepted {
		t.Fatalf("write grant rejected: %+v", got)
	}
	select {
	case result := <-grantResult:
		if result.Action != platform.RealmGrantDialogWrite || result.RealmID != "realm-read" {
			t.Fatalf("realm grant result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("realm grant browser did not return")
	}
}

func TestRepositoryServiceRejectsStaleRealmGrantContinuation(t *testing.T) {
	service := newRepositoryService()
	service.snapshot.Context = RepositoryContextProjection{ServerID: "spot", RepoID: "newer"}
	service.pendingGrants = repositoryContextKey("spot", "older")
	result, err := (repositoryRealmGrantBrowserAdapter{service: service}).ShowRealmGrants(context.Background(), platform.RealmGrantDialogRequest{Recipients: []platform.RealmGrantRecipient{{RealmID: "realm"}}})
	if err != nil || result.Action != platform.RealmGrantDialogClose || service.Snapshot().Revision != 0 {
		t.Fatalf("stale realm grants = result %+v err %v snapshot %+v", result, err, service.Snapshot())
	}
}

func TestRepositoryServiceValidatesUploadChannelChoices(t *testing.T) {
	service := newRepositoryService()
	service.snapshot.Context = RepositoryContextProjection{ServerID: "spot", RepoID: "docs", Name: "Dokumenty"}
	service.pendingUploads = repositoryContextKey("spot", "docs")
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	resultCh := make(chan platform.UploadChannelDialogResult, 1)
	go func() {
		result, _ := (repositoryUploadChannelBrowserAdapter{service: service}).ShowUploadChannels(context.Background(), platform.UploadChannelDialogRequest{
			Title: "Półki", Channels: []platform.UploadChannelSummary{
				{ChannelID: "active", Address: "acme/inbox", State: "aktywne", Recipients: "a@example.net"},
				{ChannelID: "revoked", Address: "acme/old", State: "cofnięte"},
			},
		})
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("upload channels window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "uploads" || len(snapshot.Uploads) != 2 || !snapshot.Uploads[0].CanEdit || snapshot.Uploads[1].CanEdit {
		t.Fatalf("upload projection = %+v", snapshot)
	}
	if got := service.ChooseUpload(RepositoryChoice{Action: "edit", ServerID: "spot", RepoID: "docs", ChannelID: "revoked"}); got.Accepted || got.Code != "upload_channel_action_unavailable" {
		t.Fatalf("revoked upload edit accepted: %+v", got)
	}
	if got := service.ChooseUpload(RepositoryChoice{Action: "revoke", ServerID: "spot", RepoID: "docs", ChannelID: "active"}); !got.Accepted {
		t.Fatalf("active upload revoke rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.UploadChannelDialogRevoke || result.ChannelID != "active" {
			t.Fatalf("upload result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("upload browser did not return")
	}
}

func TestRepositoryServiceValidatesQuarantineChoices(t *testing.T) {
	service := newRepositoryService()
	service.snapshot.Context = RepositoryContextProjection{ServerID: "spot", RepoID: "trash", Name: "Kwarantanna"}
	service.pendingQuarantine = repositoryContextKey("spot", "trash")
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	resultCh := make(chan platform.QuarantineDialogResult, 1)
	go func() {
		result, _ := (repositoryQuarantineBrowserAdapter{service: service}).ShowQuarantine(context.Background(), platform.QuarantineDialogRequest{
			Title: "Kwarantanna", Items: []platform.QuarantineItem{
				{UploadID: "u1", OriginalName: "wirus.exe", Size: 12, RemainingHours: 40, AVVerdict: "Eicar"},
			},
		})
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("quarantine window was not shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "quarantine" || len(snapshot.Quarantine) != 1 || snapshot.Quarantine[0].SizeLabel == "" {
		t.Fatalf("quarantine projection = %+v", snapshot)
	}
	if got := service.ChooseQuarantine(RepositoryChoice{Action: "fetch", ServerID: "spot", RepoID: "docs", UploadID: "u1"}); got.Accepted || got.Code != "repository_context_changed" {
		t.Fatalf("foreign quarantine context accepted: %+v", got)
	}
	if got := service.ChooseQuarantine(RepositoryChoice{Action: "fetch", ServerID: "spot", RepoID: "trash", UploadID: "missing"}); got.Accepted || got.Code != "quarantine_action_unavailable" {
		t.Fatalf("missing quarantine fetch accepted: %+v", got)
	}
	if got := service.ChooseQuarantine(RepositoryChoice{Action: "hide", ServerID: "spot", RepoID: "trash", UploadID: "u1"}); !got.Accepted {
		t.Fatalf("quarantine hide rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.QuarantineDialogHide || result.UploadID != "u1" {
			t.Fatalf("quarantine result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("quarantine browser did not return")
	}
}

func TestRepositoryServiceAllowsDirectQuarantineEntry(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	resultCh := make(chan platform.QuarantineDialogResult, 1)
	go func() {
		result, _ := (repositoryQuarantineBrowserAdapter{service: service}).ShowQuarantine(context.Background(), platform.QuarantineDialogRequest{
			Title: "Kwarantanna", ServerID: "spot", RepoID: "trash", RepositoryName: "Odrzuty", DirectEntry: true,
		})
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("direct quarantine window was not shown")
	}
	if snapshot := service.Snapshot(); snapshot.Mode != "quarantine" || snapshot.Context.ServerID != "spot" || snapshot.Context.RepoID != "trash" || snapshot.Context.Name != "Odrzuty" {
		t.Fatalf("direct quarantine projection = %+v", snapshot)
	}
	service.Cancel()
	select {
	case <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("direct quarantine browser did not close")
	}
}

func TestRepositoryServiceReturnsSingleRepoForConnect(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	request := platform.SettingsDialogRequest{
		FocusRepoID: "remote",
		Servers:     []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "remote", Name: "Zdalny", CanConnect: true}}}},
	}
	resultCh := make(chan platform.SettingsDialogResult, 1)
	go func() {
		result, _ := (repositorySettingsBrowserAdapter{service: service}).ShowSettings(context.Background(), request)
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("repository connect window was not shown")
	}
	if got := service.ChooseAction(RepositoryChoice{Action: "connect_repositories", ServerID: "spot", RepoID: "remote"}); !got.Accepted {
		t.Fatalf("connect rejected: %+v", got)
	}
	select {
	case result := <-resultCh:
		if result.Action != platform.SettingsDialogConnectRepos || len(result.RepoIDs) != 1 || result.RepoIDs[0] != "remote" {
			t.Fatalf("connect result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("repository connect browser did not return")
	}
}

func TestRepositoryServiceRejectsUnscopedRequests(t *testing.T) {
	service := newRepositoryService()
	settings, err := (repositorySettingsBrowserAdapter{service: service}).ShowSettings(context.Background(), platform.SettingsDialogRequest{Servers: []platform.SettingsServer{{ID: "spot"}}})
	if err != nil || settings.Action != platform.SettingsDialogClose {
		t.Fatalf("unscoped settings = %+v, %v", settings, err)
	}
	shares, err := (repositoryPublicShareBrowserAdapter{service: service}).ShowPublicShares(context.Background(), platform.PublicShareDialogRequest{ServerID: "spot"})
	if err != nil || shares.Action != platform.PublicShareDialogClose {
		t.Fatalf("unscoped shares = %+v, %v", shares, err)
	}
}
