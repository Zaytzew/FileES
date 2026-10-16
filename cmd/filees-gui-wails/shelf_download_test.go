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

func TestShelfBatchChoiceRequiresCurrentNonemptyAuthorizedSnapshot(t *testing.T) {
	for _, action := range []string{"import_all", "refresh"} {
		s := &RepositoryService{}
		s.snapshot = RepositorySnapshot{Mode: "shelf", Context: RepositoryContextProjection{ServerID: "office", RepoID: "parent"}, FocusChannelID: "channel", Shelf: []ShelfItemProjection{{UploadID: "one"}, {UploadID: "two"}}}
		s.shelf = &repositoryShelfSession{result: make(chan platform.ShelfDialogResult, 1)}
		choice := RepositoryChoice{ServerID: "office", RepoID: "parent", ChannelID: "channel", Action: action}
		if action == "import_all" && s.ChooseShelf(choice).Accepted {
			t.Fatal("batch without authority")
		}
		s.snapshot.ShelfCanImport = true
		choice.ChannelID = "foreign"
		if s.ChooseShelf(choice).Accepted {
			t.Fatal("foreign channel")
		}
		choice.ChannelID = "channel"
		choice.UploadID = "one"
		if s.ChooseShelf(choice).Accepted {
			t.Fatal("ambiguous batch selection")
		}
		choice.UploadID = ""
		if !s.ChooseShelf(choice).Accepted {
			t.Fatal("valid choice refused", action)
		}
		if s.ChooseShelf(choice).Accepted {
			t.Fatal("duplicate accepted")
		}
		if result := <-s.shelf.result; string(result.Action) != action || result.UploadID != "" {
			t.Fatal(result)
		}
	}
}
