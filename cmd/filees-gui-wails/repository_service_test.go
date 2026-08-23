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
