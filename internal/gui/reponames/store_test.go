package reponames

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetSurvivesReopenAndEmptyRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui", "repo-names.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("office", "docs", "  Umowy 2026  "); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("home", "docs", "Dom"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := reopened.Name("office", "docs"); !ok || name != "Umowy 2026" {
		t.Fatalf("office/docs = %q %v", name, ok)
	}
	if name, _ := reopened.Name("home", "docs"); name != "Dom" {
		t.Fatalf("same repo ID on another server = %q", name)
	}
	if err := reopened.Set("office", "docs", " "); err != nil {
		t.Fatal(err)
	}
	again, _ := Open(path)
	if _, ok := again.Name("office", "docs"); ok {
		t.Fatal("an empty name must bring back the repository's own name")
	}
}

func TestRejectsNamesThatAreNotOneLine(t *testing.T) {
	store, _ := Open(filepath.Join(t.TempDir(), "repo-names.json"))
	for _, name := range []string{"a\nb", "tab\there", strings.Repeat("ż", MaxRunes+1)} {
		if err := store.Set("s", "r", name); err != ErrInvalidName {
			t.Fatalf("Set(%q) = %v", name, err)
		}
	}
	if _, ok := store.Name("s", "r"); ok {
		t.Fatal("a refused name must not be kept")
	}
}

func TestDamagedFileDoesNotStopTheClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-names.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err == nil || store == nil {
		t.Fatalf("Open() = %v, %v; want a usable empty store and the error", store, err)
	}
	if err := store.Set("s", "r", "Nowa"); err != nil {
		t.Fatal(err)
	}
}
