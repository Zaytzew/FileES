package main

import (
	"context"
	"errors"
	"testing"

	"filees/internal/gui/platform"
)

func TestWailsFolderPickerReturnsNativeSelectionAndCancellation(t *testing.T) {
	picker := wailsFolderPicker{selectDirectory: func(title, initialDir string) (string, error) {
		if title != "Folder" || initialDir != "/data" {
			t.Fatalf("native request = %q, %q", title, initialDir)
		}
		return "/data/projekt/..", nil
	}}
	result, err := picker.PickFolder(context.Background(), platform.PickFolderRequest{Title: "Folder", InitialDir: "/data/./"})
	if err != nil || result.Cancelled || result.Path != "/data" {
		t.Fatalf("PickFolder() = %#v, %v", result, err)
	}

	picker.selectDirectory = func(string, string) (string, error) { return "", nil }
	result, err = picker.PickFolder(context.Background(), platform.PickFolderRequest{InitialDir: "/data"})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFolder() = %#v, %v", result, err)
	}
}

func TestWailsFolderPickerReportsNativeFailure(t *testing.T) {
	want := errors.New("native dialog failed")
	picker := wailsFolderPicker{selectDirectory: func(string, string) (string, error) { return "", want }}
	_, err := picker.PickFolder(context.Background(), platform.PickFolderRequest{InitialDir: "/data"})
	if !platform.IsFailure(err, platform.FailureOperational) || !errors.Is(err, want) {
		t.Fatalf("PickFolder() error = %v", err)
	}
}

func TestWailsFilePickerReturnsValidatedNativeSelections(t *testing.T) {
	picker := wailsFolderPicker{selectFiles: func(title, initialDir string, multiple bool) ([]string, error) {
		if title != "Lock" || initialDir != "/data/repo" || !multiple {
			t.Fatalf("native request = %q, %q, multiple=%v", title, initialDir, multiple)
		}
		return []string{"/data/repo/a.dwg", "/data/repo/dir/../a.dwg", "/data/repo/b.dwg"}, nil
	}}
	result, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{
		Title: "Lock", Root: "/data/repo", InitialDir: "/data/repo/./", AllowMultiple: true,
	})
	if err != nil || result.Cancelled || len(result.Paths) != 2 || result.Paths[0] != "/data/repo/a.dwg" || result.Paths[1] != "/data/repo/b.dwg" {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}

	picker.selectFiles = func(string, string, bool) ([]string, error) { return nil, nil }
	result, err = picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: "/data/repo"})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFiles() = %#v, %v", result, err)
	}
}

func TestWailsFilePickerEnforcesRepositoryBoundary(t *testing.T) {
	picker := wailsFolderPicker{selectFiles: func(string, string, bool) ([]string, error) {
		return []string{"/data/other/file.dwg"}, nil
	}}
	_, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: "/data/repo"})
	if !platform.IsFailure(err, platform.FailureOperational) {
		t.Fatalf("outside-root selection error = %v", err)
	}

	called := false
	picker.selectFiles = func(string, string, bool) ([]string, error) {
		called = true
		return nil, nil
	}
	_, err = picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: "/data/repo", InitialDir: "/data/other"})
	if !platform.IsFailure(err, platform.FailureOperational) || called {
		t.Fatalf("outside-root initial directory error = %v, dialog called=%v", err, called)
	}
}

func TestWailsFilePickerAllowsAbsolutePresentationAssets(t *testing.T) {
	picker := wailsFolderPicker{selectFiles: func(title, initialDir string, multiple bool) ([]string, error) {
		if title != "Logo" || initialDir != "/home/test" || multiple {
			t.Fatalf("native request = %q, %q, multiple=%v", title, initialDir, multiple)
		}
		return []string{"/tmp/../tmp/logo.png"}, nil
	}}
	result, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{
		Title: "Logo", InitialDir: "/home/test", AllowOutsideRoot: true,
	})
	if err != nil || len(result.Paths) != 1 || result.Paths[0] != "/tmp/logo.png" {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
}

func TestWailsFilePickerReportsNativeFailure(t *testing.T) {
	want := errors.New("native dialog failed")
	picker := wailsFolderPicker{selectFiles: func(string, string, bool) ([]string, error) { return nil, want }}
	_, err := picker.PickFiles(context.Background(), platform.PickFilesRequest{Root: "/data/repo"})
	if !platform.IsFailure(err, platform.FailureOperational) || !errors.Is(err, want) {
		t.Fatalf("PickFiles() error = %v", err)
	}
}
