package commit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/talk"
)

func renameService() *Service {
	return &Service{Logger: talk.With("portable-rename-test")}
}

// The ordinary path: the object is refused, the person supplies a name that
// works, and the condition has nothing left to stand on.
func TestRenamingARefusedObjectSucceeds(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "Tekst.txt"), []byte("umowa"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "tekst.txt"), []byte("brudnopis"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := renameService()

	if err := service.RenameUnportable(wc, "tekst.txt", "brudnopis.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wc, "brudnopis.txt")); err != nil {
		t.Fatalf("renamed object is not there: %v", err)
	}
	if gateProblem(wc, "brudnopis.txt") != nil {
		t.Fatal("the new name must not be refused in turn")
	}
}

// A name with the same defect, or a new one, is rejected before the rename
// happens. Renaming first would leave two problems instead of one.
func TestARenameIntoAnotherImpossibleNameIsRefused(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "Tekst.txt"), []byte("umowa"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "tekst.txt"), []byte("brudnopis"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := renameService()

	for _, name := range []string{"CON.txt", "a:b.txt", "kropka.", "pod/katalog.txt"} {
		if err := service.RenameUnportable(wc, "tekst.txt", name); !errors.Is(err, ErrRenameNameUnportable) {
			t.Errorf("rename to %q = %v, want ErrRenameNameUnportable", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wc, "tekst.txt")); err != nil {
		t.Fatal("a refused rename must leave the object where it was")
	}
}

// The new name must not walk into the collision the old one had.
func TestARenameOntoAnExistingSiblingIsRefused(t *testing.T) {
	wc := t.TempDir()
	for _, name := range []string{"Tekst.txt", "tekst.txt", "rysunek.dwg"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := renameService()

	if err := service.RenameUnportable(wc, "tekst.txt", "Rysunek.DWG"); !errors.Is(err, ErrRenameTargetExists) {
		t.Fatalf("rename = %v, want ErrRenameTargetExists", err)
	}
}

// Nothing to fix is its own answer, not a silent success. Renaming a path the
// gate never refused would move a file behind the person's back.
func TestRenamingAPathTheGateAcceptsIsRefused(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "rysunek.dwg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := renameService()

	if err := service.RenameUnportable(wc, "rysunek.dwg", "inny.dwg"); !errors.Is(err, ErrRenameNotRefused) {
		t.Fatalf("rename = %v, want ErrRenameNotRefused", err)
	}
	if _, err := os.Stat(filepath.Join(wc, "rysunek.dwg")); err != nil {
		t.Fatal("the object must not have moved")
	}
}

// The object being renamed is not its own obstacle: a case-only rename of the
// refused object itself must be possible, since that is often the whole fix.
func TestTheRefusedObjectIsNotItsOwnObstacle(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "CON.txt"), []byte("x"), 0o600); err != nil {
		t.Skip("this system will not create a reserved device name, so the case cannot be set up here")
	}
	service := renameService()

	if err := service.RenameUnportable(wc, "CON.txt", "CONtener.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
}
