package main

import (
	"filees/internal/gui/platform"
	"testing"
)

func TestShelfChoiceRequiresCurrentChannelAndListedUpload(t *testing.T) {
	s := &RepositoryService{}
	s.snapshot = RepositorySnapshot{Mode: "shelf", Context: RepositoryContextProjection{ServerID: "office", RepoID: "parent"}, FocusChannelID: "channel", Shelf: []ShelfItemProjection{{UploadID: "upload"}}}
	s.shelf = &repositoryShelfSession{result: make(chan platform.ShelfDialogResult, 1)}
	choice := RepositoryChoice{ServerID: "office", RepoID: "parent", ChannelID: "other", UploadID: "upload", Action: "fetch"}
	if s.ChooseShelf(choice).Accepted {
		t.Fatal("foreign channel accepted")
	}
	choice.ChannelID, choice.UploadID = "channel", "foreign"
	if s.ChooseShelf(choice).Accepted {
		t.Fatal("foreign upload accepted")
	}
	choice.UploadID = "upload"
	if !s.ChooseShelf(choice).Accepted {
		t.Fatal("displayed selection refused")
	}
	if s.ChooseShelf(choice).Accepted {
		t.Fatal("double click accepted")
	}
	if result := <-s.shelf.result; result.UploadID != "upload" || result.Action != platform.ShelfDialogFetch {
		t.Fatalf("choice: %+v", result)
	}
}

func TestShelfImportChoiceRequiresDaemonCapability(t *testing.T) {
	s := &RepositoryService{}
	s.snapshot = RepositorySnapshot{Mode: "shelf", Context: RepositoryContextProjection{ServerID: "office", RepoID: "parent"}, FocusChannelID: "channel", Shelf: []ShelfItemProjection{{UploadID: "upload"}}}
	s.shelf = &repositoryShelfSession{result: make(chan platform.ShelfDialogResult, 1)}
	choice := RepositoryChoice{ServerID: "office", RepoID: "parent", ChannelID: "channel", UploadID: "upload", Action: "import"}
	if s.ChooseShelf(choice).Accepted {
		t.Fatal("import without parent capability accepted")
	}
	s.snapshot.ShelfCanImport = true
	if !s.ChooseShelf(choice).Accepted {
		t.Fatal("authorized import refused")
	}
	if result := <-s.shelf.result; result.Action != platform.ShelfDialogImport {
		t.Fatal("import became ordinary download")
	}
}
