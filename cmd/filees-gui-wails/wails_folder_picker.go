package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/gui/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type wailsFolderPicker struct {
	selectDirectory func(title, initialDir string) (string, error)
	selectFiles     func(title, initialDir string, multiple bool) ([]string, error)
}

func newWailsFolderPicker(dialogs *application.DialogManager) wailsFolderPicker {
	return wailsFolderPicker{
		selectDirectory: func(title, initialDir string) (string, error) {
			return dialogs.OpenFile().
				CanChooseFiles(false).
				CanChooseDirectories(true).
				CanCreateDirectories(true).
				SetTitle(title).
				SetDirectory(initialDir).
				PromptForSingleSelection()
		},
		selectFiles: func(title, initialDir string, multiple bool) ([]string, error) {
			dialog := dialogs.OpenFile().
				CanChooseFiles(true).
				CanChooseDirectories(false).
				SetTitle(title).
				SetDirectory(initialDir)
			if multiple {
				return dialog.PromptForMultipleSelection()
			}
			selected, err := dialog.PromptForSingleSelection()
			if err != nil || selected == "" {
				return nil, err
			}
			return []string{selected}, nil
		},
	}
}

func (picker wailsFolderPicker) PickFolder(ctx context.Context, request platform.PickFolderRequest) (platform.PickFolderResult, error) {
	if err := ctx.Err(); err != nil {
		return platform.PickFolderResult{}, err
	}
	initialDir := request.InitialDir
	if initialDir == "" {
		initialDir, _ = os.UserHomeDir()
	}
	if !filepath.IsAbs(initialDir) {
		return platform.PickFolderResult{}, platform.NewOperationalFailure("folder_picker", errors.New("initial directory must be absolute"))
	}
	selected, err := picker.selectDirectory(request.Title, filepath.Clean(initialDir))
	if err != nil {
		return platform.PickFolderResult{}, platform.NewOperationalFailure("folder_picker", err)
	}
	if err := ctx.Err(); err != nil {
		return platform.PickFolderResult{}, err
	}
	if selected == "" {
		return platform.PickFolderResult{Cancelled: true}, nil
	}
	if !filepath.IsAbs(selected) {
		return platform.PickFolderResult{}, platform.NewOperationalFailure("folder_picker", errors.New("selected directory must be absolute"))
	}
	return platform.PickFolderResult{Path: filepath.Clean(selected)}, nil
}

func (picker wailsFolderPicker) PickFiles(ctx context.Context, request platform.PickFilesRequest) (platform.PickFilesResult, error) {
	if err := ctx.Err(); err != nil {
		return platform.PickFilesResult{}, err
	}
	initialDir, err := pickerInitialDirectory(request)
	if err != nil {
		return platform.PickFilesResult{}, platform.NewOperationalFailure("file_picker", err)
	}
	if picker.selectFiles == nil {
		return platform.PickFilesResult{}, platform.NewUnavailable("file_picker", errors.New("native file dialog is unavailable"))
	}
	selected, err := picker.selectFiles(request.Title, initialDir, request.AllowMultiple)
	if err != nil {
		return platform.PickFilesResult{}, platform.NewOperationalFailure("file_picker", err)
	}
	if err := ctx.Err(); err != nil {
		return platform.PickFilesResult{}, err
	}
	if len(selected) == 0 {
		return platform.PickFilesResult{Cancelled: true}, nil
	}
	if !request.AllowMultiple && len(selected) > 1 {
		selected = selected[:1]
	}
	paths, err := validateWailsPickedFiles(request, selected)
	if err != nil {
		return platform.PickFilesResult{}, platform.NewOperationalFailure("file_picker", err)
	}
	return platform.PickFilesResult{Paths: paths}, nil
}

func pickerInitialDirectory(request platform.PickFilesRequest) (string, error) {
	initialDir := request.InitialDir
	if initialDir == "" {
		initialDir = request.Root
	}
	if initialDir == "" && request.AllowOutsideRoot {
		initialDir, _ = os.UserHomeDir()
	}
	if request.AllowOutsideRoot {
		if strings.TrimSpace(initialDir) == "" || !filepath.IsAbs(initialDir) {
			return "", errors.New("initial directory must be absolute")
		}
		return filepath.Clean(initialDir), nil
	}
	validated, err := platform.ValidatePickedPaths(request.Root, []string{initialDir})
	if err != nil {
		return "", err
	}
	return validated[0], nil
}

func validateWailsPickedFiles(request platform.PickFilesRequest, selected []string) ([]string, error) {
	if !request.AllowOutsideRoot {
		return platform.ValidatePickedPaths(request.Root, selected)
	}
	paths := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, path := range selected {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return nil, errors.New("selected path must be absolute")
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	return paths, nil
}

var _ platform.FolderPicker = wailsFolderPicker{}
var _ platform.FilePicker = wailsFolderPicker{}
