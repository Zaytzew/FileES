package actions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
)

func TestEditingPublicShareWithMissingSourceStartsPickerAtWorkingCopy(t *testing.T) {
	wc := t.TempDir()
	var initial string
	var title string
	fake := &platformtest.Fake{PickFolderFunc: func(_ context.Context, request platform.PickFolderRequest) (platform.PickFolderResult, error) {
		initial = request.InitialDir
		title = request.Title
		return platform.PickFolderResult{Cancelled: true}, nil
	}}
	c := &Controller{cfg: Config{FolderPicker: fake, Prompter: fake}}
	repo := app.RepoViewModel{ID: "docs", Attached: true, LocalPath: wc}
	if _, accepted := c.collectPublicShareDeclaration(context.Background(), repo, &PublicShareSummary{SourceRoot: "removed/folder"}); accepted {
		t.Fatal("cancelled picker accepted declaration")
	}
	if initial != wc {
		t.Fatalf("picker initial directory = %q, want working copy %q", initial, wc)
	}
	if title != "Folder udziału już nie istnieje — wskaż nowy folder" {
		t.Fatalf("missing-source picker title = %q", title)
	}

	existing := filepath.Join(wc, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	c.collectPublicShareDeclaration(context.Background(), repo, &PublicShareSummary{SourceRoot: "existing"})
	if initial != existing {
		t.Fatalf("existing source was not retained: %q", initial)
	}
}
