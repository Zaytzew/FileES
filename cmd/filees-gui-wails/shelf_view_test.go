package main

import (
	"context"
	"testing"
	"time"

	"filees/internal/gui/platform"
)

func shelfRequest() platform.ShelfDialogRequest {
	return platform.ShelfDialogRequest{
		Title: "Zawartość półki", ServerID: "spot", RepoID: "docs", RepositoryName: "Dokumenty",
		ChannelID: "channel-1", ShelfName: "oferta-a",
		Items: []platform.ShelfItem{{
			UploadID: "upload-1", RepoPath: "Opinia-Lodz.pdf", OriginalName: "Opinia Łódź.pdf",
			Size: 2048, Revision: 42, AcceptedAt: "2026-09-12T10:00:00Z",
		}},
	}
}

// Close is the only way out of the shelf view, so Cancel has to release it.
// If it does not, the controller goroutine that opened the shelf blocks for
// the life of the process and the channel dialog behind it never comes back.
func TestRepositoryServiceCancelReleasesAWaitingShelf(t *testing.T) {
	service := newRepositoryService()
	shown := make(chan struct{}, 1)
	service.attachPresentation(func() { shown <- struct{}{} }, func() {})
	resultCh := make(chan platform.ShelfDialogResult, 1)
	go func() {
		result, _ := (repositoryShelfBrowserAdapter{service: service}).ShowShelf(context.Background(), shelfRequest())
		resultCh <- result
	}()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("the shelf view was never shown")
	}
	snapshot := service.Snapshot()
	if snapshot.Mode != "shelf" || snapshot.ShelfName != "oferta-a" || snapshot.FocusChannelID != "channel-1" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(snapshot.Shelf) != 1 || snapshot.Shelf[0].RepoPath != "Opinia-Lodz.pdf" {
		t.Fatalf("shelf=%+v", snapshot.Shelf)
	}
	// The size label is formatted host-side, the same way quarantine does it,
	// so two views of one number cannot disagree.
	if snapshot.Shelf[0].SizeLabel != "2 KB" {
		t.Fatalf("sizeLabel=%q", snapshot.Shelf[0].SizeLabel)
	}
	service.Cancel()
	select {
	case result := <-resultCh:
		if result.Action != platform.ShelfDialogClose {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not release the shelf; the controller would block forever")
	}
}

// A second shelf opening while one is waiting releases the first rather than
// leaving its goroutine parked on a channel nobody will ever write to.
func TestRepositoryServiceSecondShelfReleasesTheFirst(t *testing.T) {
	service := newRepositoryService()
	service.attachPresentation(func() {}, func() {})
	first := make(chan platform.ShelfDialogResult, 1)
	go func() {
		result, _ := (repositoryShelfBrowserAdapter{service: service}).ShowShelf(context.Background(), shelfRequest())
		first <- result
	}()
	deadline := time.Now().Add(time.Second)
	for service.Snapshot().Mode != "shelf" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := shelfRequest()
	second.ChannelID = "channel-2"
	go func() {
		_, _ = (repositoryShelfBrowserAdapter{service: service}).ShowShelf(context.Background(), second)
	}()
	select {
	case result := <-first:
		if result.Action != platform.ShelfDialogClose {
			t.Fatalf("first result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("the first shelf was left waiting when a second opened")
	}
	service.Cancel()
}

// A request without a channel is refused: a shelf is always reached through
// the channel that names it, never guessed.
func TestRepositoryServiceRefusesAShelfWithoutAChannel(t *testing.T) {
	service := newRepositoryService()
	service.attachPresentation(func() {}, func() {})
	request := shelfRequest()
	request.ChannelID = ""
	result, err := (repositoryShelfBrowserAdapter{service: service}).ShowShelf(context.Background(), request)
	if err != nil || result.Action != platform.ShelfDialogClose {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if service.Snapshot().Mode == "shelf" {
		t.Fatal("a shelf view opened without a channel")
	}
}
