package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"filees/internal/explorermenu"
)

// Explorer's selection is a path, not a repository ID and not authority. Map
// it to exactly one current attachment; only the daemon can admit its effects.
// This private entry point is not a Wails binding. The caller holds display
// admission through both confirmation and any refusal dialog.
func (service *HeadBrowserService) explorerFolder(request explorermenu.Request) error {
	refuse := func() error {
		return errors.New(service.text("explorer.virtualUnavailable", "Wybierz folder wewnątrz podłączonego folderu wirtualnego FileES. Działanie wymaga aktualnego stanu demona i dostępnego pomocnika."))
	}
	parsed, err := explorermenu.Parse([]string{request.Verb, request.Path})
	if err != nil || (parsed.Verb != explorermenu.Download && parsed.Verb != explorermenu.Release) {
		return refuse()
	}
	selection := filepath.Clean(parsed.Path)
	snapshot := service.projection()
	if !snapshot.Connected || snapshot.Stale {
		return refuse()
	}
	var match HeadBrowserRepository
	var relative string
	matches := 0
	for _, attachment := range snapshot.Repositories {
		if !attachment.Attached || !filepath.IsAbs(attachment.LocalPath) {
			continue
		}
		root := filepath.Clean(attachment.LocalPath)
		probe := selection
		if runtime.GOOS == "windows" {
			root, probe = strings.ToLower(root), strings.ToLower(probe)
		}
		rel, err := filepath.Rel(root, probe)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		matches++ // Count ordinary/private/overlapping attachments: refuse ambiguity.
		candidate, ok := service.lookup(attachment.ServerID, attachment.ID)
		if !ok || !candidate.CanBatch {
			continue
		}
		original, err := filepath.Rel(filepath.Clean(attachment.LocalPath), selection)
		if err != nil {
			continue
		}
		match, relative = candidate, filepath.ToSlash(original)
	}
	if matches != 1 || !match.CanBatch {
		return refuse()
	}
	action := "download"
	if parsed.Verb == explorermenu.Release {
		action = "release"
		if !match.CanRelease {
			return refuse()
		}
	}
	// Keep the visible repository browser untouched. A private focus lets the
	// existing folder flow recheck this attachment after confirmation without
	// borrowing, changing or racing the user's currently open browser window.
	operation := newHeadBrowserService(service.daemon, service.projection, service.render, service.text)
	operation.confirm = service.confirm
	operation.showProgress = service.showProgress
	operation.folderComplete = service.folderComplete
	operation.focus = HeadBrowserContext{ServerID: match.ServerID, RepoID: match.RepoID}
	_, handled, err := operation.folderAction(match, relative, action)
	if err != nil {
		return err
	}
	if !handled {
		return refuse()
	} // Never fall back to ordinary sparse SVN.
	return nil
}
