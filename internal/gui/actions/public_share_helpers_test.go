package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicShareObjectsStayInsideSelectedSubfolderAndPreserveIDs(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "wydanie")
	if err := os.MkdirAll(filepath.Join(selected, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".filees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, ".svn", "metadata"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".filees", "identity"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := &PublicShareSummary{Objects: []PublicShareObject{{PublicID: "1234567890abcdef", RepoPath: "wydanie/a.txt", DisplayName: "a.txt"}}}
	objects, sourceRoot, err := publicShareObjects(root, selected, current)
	if err != nil {
		t.Fatal(err)
	}
	if sourceRoot != "wydanie" || len(objects) != 1 || objects[0].RepoPath != "wydanie/a.txt" || objects[0].PublicID != "1234567890abcdef" {
		t.Fatalf("source=%q objects=%+v", sourceRoot, objects)
	}
	rootObjects, rootSource, err := publicShareObjects(root, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rootSource != "." || len(rootObjects) != 1 || rootObjects[0].RepoPath != "wydanie/a.txt" {
		t.Fatalf("root source=%q objects=%+v", rootSource, rootObjects)
	}
	if _, _, err := publicShareObjects(root, filepath.Join(root, ".filees"), nil); err == nil {
		t.Fatal("FileES metadata folder was accepted as a public source root")
	}
	if _, _, err := publicShareObjects(root, filepath.Dir(root), nil); err == nil {
		t.Fatal("folder outside working copy was accepted")
	}
	empty := filepath.Join(root, "placeholder")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	emptyObjects, emptySource, err := publicShareObjects(root, empty, nil)
	if err != nil {
		t.Fatal(err)
	}
	if emptySource != "placeholder" || len(emptyObjects) != 0 {
		t.Fatalf("empty source=%q objects=%+v", emptySource, emptyObjects)
	}
}

func TestSplitRecipientsNormalizesSeparatorsAndDuplicates(t *testing.T) {
	got := splitRecipients("a@example.com; B@example.com, a@example.com\n")
	if len(got) != 2 || got[0] != "a@example.com" || got[1] != "B@example.com" {
		t.Fatalf("recipients=%v", got)
	}
}
