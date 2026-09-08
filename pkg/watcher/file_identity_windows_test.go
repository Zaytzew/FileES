package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileIdentityIsPresentAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plik.txt")
	writeFile(t, path, "zawartosc")

	first := fileIdentity(path)
	if first == "" {
		t.Fatal("no identity for an ordinary file: the native lane cannot work without one")
	}
	if !strings.HasPrefix(first, "windows:") {
		t.Fatalf("identity = %q, want the platform named", first)
	}
	if second := fileIdentity(path); second != first {
		t.Fatalf("identity is not stable: %q then %q", first, second)
	}
}

// The whole point. A rename must keep the identity, or the watcher cannot tell
// a move from a delete-and-add, and record-move is never reached.
func TestFileIdentitySurvivesARename(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(dir, "stara-nazwa.txt")
	after := filepath.Join(dir, "nowa nazwa.txt")
	writeFile(t, before, "ta sama zawartosc")

	original := fileIdentity(before)
	if original == "" {
		t.Fatal("no identity to begin with")
	}
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	if moved := fileIdentity(after); moved != original {
		t.Fatalf("identity changed across a rename: %q then %q", original, moved)
	}
}

// Two files must never share an identity, or two unrelated objects look like
// one thing moving.
func TestFileIdentityDistinguishesFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.txt")
	second := filepath.Join(dir, "b.txt")
	writeFile(t, first, "identyczna tresc")
	writeFile(t, second, "identyczna tresc")

	if fileIdentity(first) == fileIdentity(second) {
		t.Fatal("two distinct files share an identity")
	}
}

// The files this product watches are open in AutoCAD and Word for hours. An
// identity readable only when nobody is editing would be missing exactly when
// a rename is most likely.
func TestFileIdentityWorksWhileTheFileIsHeldOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otwarty.dwg")
	writeFile(t, path, "rysunek")

	held, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	if fileIdentity(path) == "" {
		t.Fatal("no identity while the file is open elsewhere")
	}
}

// Working copies here are real project trees; one measured on this machine was
// 337 characters.
func TestFileIdentityHandlesLongPaths(t *testing.T) {
	dir := t.TempDir()
	deep := dir
	for len(deep) < 300 {
		deep = filepath.Join(deep, "katalog-o-dosc-dlugiej-nazwie")
	}
	path := filepath.Join(deep, "gleboko.txt")
	writeFile(t, path, "daleko stad")

	if fileIdentity(path) == "" {
		t.Fatalf("no identity for a %d character path", len(path))
	}
}

// Directories and missing paths carry no ancestry to follow.
func TestFileIdentityRefusesWhatItCannotIdentify(t *testing.T) {
	dir := t.TempDir()
	if got := fileIdentity(dir); got != "" {
		t.Fatalf("directory has an identity: %q", got)
	}
	if got := fileIdentity(filepath.Join(dir, "nie-ma-mnie.txt")); got != "" {
		t.Fatalf("missing file has an identity: %q", got)
	}
}
