package platform

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// testRoot builds an absolute fixture root for the host platform. These tests
// used to hard-code filepath.Join(filepath.Separator, ...), which yields
// "\wc\repo" on Windows -- a rooted but *drive-relative* path that
// filepath.IsAbs correctly rejects, so every path fixture was refused before
// the assertion under test was ever reached. On anything but Windows this is
// exactly the previous value, so the fixtures are unchanged there.
func testRoot(elem ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

type fakeExitCodeError int

func (e fakeExitCodeError) Error() string { return "exit" }
func (e fakeExitCodeError) ExitCode() int { return int(e) }

func TestCommandCancelledRecognisesZenityAndYadCancelCodes(t *testing.T) {
	// 1: zenity and yad both use this for an explicit Cancel button.
	// 252: yad-only, dialog closed via Esc or the window's close control --
	// zenity has no equivalent code, so this case never fires there.
	for _, code := range []int{1, 252} {
		if !commandCancelled(fakeExitCodeError(code)) {
			t.Errorf("commandCancelled(exit %d) = false, want true", code)
		}
	}
	if commandCancelled(fakeExitCodeError(70)) {
		t.Error("commandCancelled(exit 70) = true, want false -- 70 is yad's --timeout code, not a user cancel")
	}
	if commandCancelled(errors.New("no exit code")) {
		t.Error("commandCancelled on a non-exit error = true, want false")
	}
}

// TestYadSelectionStripsExactlyOneTrailingSeparator is the regression test
// for a real bug found live: yad appends a trailing "|" field separator
// after the last printed --print-column value, even for a single column
// ("add_folder" comes back as "add_folder|"). Every exact-match/strings.Cut
// consumer in this file broke silently on this -- the dialog ran and exited
// cleanly, the caller just never recognised the selection. Only one
// trailing separator is yad's own artifact; a second one is the caller's
// own intentional trailing delimiter (e.g. server.ID+"|" for a folder-less
// row) and must survive.
func TestYadSelectionStripsExactlyOneTrailingSeparator(t *testing.T) {
	cases := []struct{ in, want string }{
		{"add_folder|", "add_folder"},
		{"add_folder", "add_folder"},
		{"biuro|8c3ecb60-a02f|", "biuro|8c3ecb60-a02f"},
		{"biuro||", "biuro|"},
		{"  add_folder|  ", "add_folder"},
		{"", ""},
	}
	for _, c := range cases {
		if got := yadSelection([]byte(c.in)); got != c.want {
			t.Errorf("yadSelection(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidatePickedPaths(t *testing.T) {
	root := testRoot("wc", "repo")
	first := filepath.Join(root, "sub", "..", "a.dwg")
	second := filepath.Join(root, "b.dwg")

	got, err := ValidatePickedPaths(root, []string{first, first, second})
	if err != nil {
		t.Fatalf("ValidatePickedPaths: %v", err)
	}
	want := []string{filepath.Clean(first), second}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestValidatePickedPathsRejectsOutsideRoot(t *testing.T) {
	root := testRoot("wc", "repo")
	outside := testRoot("wc", "repo-other", "a.dwg")
	if _, err := ValidatePickedPaths(root, []string{outside}); err == nil {
		t.Fatal("expected outside-root path to be rejected")
	}
}

func TestValidatePickedPathsRejectsRelativeRootOrPath(t *testing.T) {
	absRoot := testRoot("wc", "repo")
	if _, err := ValidatePickedPaths("relative", []string{filepath.Join(absRoot, "a")}); err == nil {
		t.Fatal("expected relative root to be rejected")
	}
	if _, err := ValidatePickedPaths(absRoot, []string{"relative"}); err == nil {
		t.Fatal("expected relative path to be rejected")
	}
}
