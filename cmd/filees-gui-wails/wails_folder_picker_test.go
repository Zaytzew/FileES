package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"filees/internal/gui/platform"
)

func TestWailsFolderPickerReturnsNativeSelectionAndCancellation(t *testing.T) {
	root := t.TempDir()
	picker := wailsFolderPicker{selectDirectory: func(title, initialDir string) (string, error) {
		if title != "Folder" || initialDir != root {
			t.Fatalf("native request = %q, %q", title, initialDir)
		}
		return root + "/projekt/..", nil
	}}
	result, err := picker.PickFolder(context.Background(), platform.PickFolderRequest{Title: "Folder", InitialDir: root + "/./"})
	if err != nil || result.Cancelled || result.Path != root {
		t.Fatalf("PickFolder() = %#v, %v", result, err)
	}

	picker.selectDirectory = func(string, string) (string, error) { return "", nil }
	result, err = picker.PickFolder(context.Background(), platform.PickFolderRequest{InitialDir: root})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFolder() = %#v, %v", result, err)
	}
}

func TestWailsFolderPickerReportsNativeFailure(t *testing.T) {
	want := errors.New("native dialog failed")
	picker := wailsFolderPicker{selectDirectory: func(string, string) (string, error) { return "", want }}
	_, err := picker.PickFolder(context.Background(), platform.PickFolderRequest{InitialDir: t.TempDir()})
	if !platform.IsFailure(err, platform.FailureOperational) || !errors.Is(err, want) {
		t.Fatalf("PickFolder() error = %v", err)
	}
}

func TestWailsFilePickerReturnsValidatedNativeSelections(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "a.dwg"), filepath.Join(root, "b.dwg")
	picker := wailsFolderPicker{selectFiles: func(title, initialDir string, multiple bool) ([]string, error) {
		if title != "Lock" || initialDir != root || !multiple {
			t.Fatalf("native request = %q, %q, multiple=%v", title, initialDir, multiple)
		}
		return []string{first, root + "/dir/../a.dwg", second}, nil
	}}
	result, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{
		Title: "Lock", Root: root, InitialDir: root + "/./", AllowMultiple: true,
	})
	if err != nil || result.Cancelled || len(result.Paths) != 2 || result.Paths[0] != first || result.Paths[1] != second {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}

	picker.selectFiles = func(string, string, bool) ([]string, error) { return nil, nil }
	result, err = picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: root})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFiles() = %#v, %v", result, err)
	}
}

func TestWailsFilePickerEnforcesRepositoryBoundary(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "other")
	called := false
	picker := wailsFolderPicker{selectFiles: func(string, string, bool) ([]string, error) {
		called = true
		return []string{filepath.Join(outside, "file.dwg")}, nil
	}}
	_, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: root})
	if !platform.IsFailure(err, platform.FailureOperational) || !called {
		t.Fatalf("outside-root selection error = %v, dialog called=%v", err, called)
	}

	called = false
	picker.selectFiles = func(string, string, bool) ([]string, error) {
		called = true
		return nil, nil
	}
	_, err = picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: root, InitialDir: outside})
	if !platform.IsFailure(err, platform.FailureOperational) || called {
		t.Fatalf("outside-root initial directory error = %v, dialog called=%v", err, called)
	}
}

func TestWailsFilePickerAllowsAbsolutePresentationAssets(t *testing.T) {
	initial := t.TempDir()
	assets := t.TempDir()
	picker := wailsFolderPicker{selectFiles: func(title, initialDir string, multiple bool) ([]string, error) {
		if title != "Logo" || initialDir != initial || multiple {
			t.Fatalf("native request = %q, %q, multiple=%v", title, initialDir, multiple)
		}
		return []string{assets + "/child/../logo.png"}, nil
	}}
	result, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{
		Title: "Logo", InitialDir: initial, AllowOutsideRoot: true,
	})
	if err != nil || len(result.Paths) != 1 || result.Paths[0] != filepath.Join(assets, "logo.png") {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
}

func TestWailsFilePickerReportsNativeFailure(t *testing.T) {
	want := errors.New("native dialog failed")
	picker := wailsFolderPicker{selectFiles: func(string, string, bool) ([]string, error) { return nil, want }}
	_, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: t.TempDir()})
	if !platform.IsFailure(err, platform.FailureOperational) || !errors.Is(err, want) {
		t.Fatalf("PickFiles() error = %v", err)
	}
}
