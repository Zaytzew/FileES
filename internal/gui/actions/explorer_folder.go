package actions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
)

// canonicalExplorerFolder reads directory metadata only, never document bytes.
func canonicalExplorerFolder(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", false
	}
	path, err = filepath.EvalSymlinks(path)
	return filepath.Clean(path), err == nil
}

func explorerComparable(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func explorerPathsOverlap(a, b string) bool {
	a, b = explorerComparable(a), explorerComparable(b)
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, strings.TrimRight(b, sep)+sep) || strings.HasPrefix(b, strings.TrimRight(a, sep)+sep)
}

func explorerAddAllowed(vm app.ViewModel, path string) bool {
	if !vm.Connected || vm.Stale || filepath.Clean(path) == filepath.VolumeName(path)+string(filepath.Separator) {
		return false
	}
	if _, err := os.Lstat(filepath.Join(path, ".svn")); !os.IsNotExist(err) {
		return false
	}
	for _, repo := range vm.Repos {
		if repo.LocalPath == "" {
			continue
		}
		root := repo.LocalPath
		if canonical, ok := canonicalExplorerFolder(root); ok {
			root = canonical
		}
		if explorerPathsOverlap(path, root) {
			return false
		}
	}
	return true
}

func (c *Controller) explorerUnavailable(ctx context.Context, path string) {
	if c.cfg.Prompter == nil {
		return
	}
	_ = c.cfg.Prompter.ShowInfo(ctx, platform.InfoRequest{
		PresentationKey: "explorer.unavailable", PresentationArgs: map[string]string{"path": path},
		Title: c.uiText("explorer.unavailable.title", "Działanie jest niedostępne"),
		Text:  path + "\n" + c.uiText("explorer.unavailable.text", "Wybierz zwykły folder do dodania albo dokładny korzeń podłączonego folderu do odłączenia. Punkty zaczepienia obsługuj w FileES. Aplikacja musi mieć aktualny stan demona i odpowiednie uprawnienia."),
	})
}

func (c *Controller) startExplorerAdd(ctx context.Context, selected string) {
	key := "explorer-add"
	if c.cfg.Prompter == nil || !c.beginOperation(key) {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation(key)
		path, ok := canonicalExplorerFolder(selected)
		vm := c.cfg.ViewModel()
		chooser, canChoose := c.cfg.Prompter.(platform.ChoicePrompter)
		if !ok || !canChoose || !explorerAddAllowed(vm, path) {
			c.explorerUnavailable(ctx, selected)
			return
		}
		request := platform.ChoiceRequest{PresentationKey: "explorer.server", PresentationArgs: map[string]string{"path": path},
			Title: c.uiText("explorer.server.title", "Dodaj do FileES"), Text: path,
			ConfirmText: c.uiText("explorer.server.confirm", "Dalej"), CancelText: c.uiText("action.cancel", "Anuluj")}
		for _, server := range vm.Servers {
			if server.Detached || !server.CanOfferRepositoryCreation() {
				continue
			}
			name := server.DisplayName
			if name == "" {
				name = server.ID
			}
			request.Options = append(request.Options, platform.ChoiceOption{Value: server.ID, Label: name})
		}
		if len(request.Options) == 0 {
			c.explorerUnavailable(ctx, selected)
			return
		}
		serverID, chosen, err := chooser.ChooseOne(ctx, request)
		if err != nil || !chosen {
			return
		}
		for _, option := range request.Options {
			if option.Value == serverID {
				c.startCreateRepositoryAt(ctx, serverID, path)
				return
			}
		}
		c.explorerUnavailable(ctx, selected)
	}()
}

func (c *Controller) startExplorerDetach(ctx context.Context, selected string) {
	if !c.beginOperation("explorer-detach") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("explorer-detach")
		c.explorerDetachFolder(ctx, selected)
	}()
}

func (c *Controller) explorerDetachFolder(ctx context.Context, selected string) {
	path, ok := canonicalExplorerFolder(selected)
	vm := c.cfg.ViewModel()
	if !ok || !vm.Connected || vm.Stale || !vm.CanDetachRepository() {
		c.startExplorerRefusal(ctx, selected)
		return
	}
	var match *app.RepoViewModel
	for _, repo := range vm.Repos {
		root, exists := canonicalExplorerFolder(repo.LocalPath)
		if !exists || explorerComparable(root) != explorerComparable(path) {
			continue
		}
		if match != nil {
			c.startExplorerRefusal(ctx, selected)
			return
		}
		copy := repo
		match = &copy
	}
	if match == nil || !match.Attached || match.Sparse || match.Purpose != "" || match.AttachmentPolicy == "required" {
		c.startExplorerRefusal(ctx, selected)
		return
	}
	c.startDetachRepositoryMode(ctx, match.ServerID, match.ID, false, true, match.LocalPath)
}

func (c *Controller) startExplorerRefusal(ctx context.Context, path string) {
	if !c.beginOperation("explorer-refusal") {
		return
	}
	c.tasks.Add(1)
	go func() {
		defer c.tasks.Done()
		defer c.endOperation("explorer-refusal")
		c.explorerUnavailable(ctx, path)
	}()
}

func (c *Controller) confirmExplorerDetach(ctx context.Context, name, path string) bool {
	confirmed, err := c.cfg.Prompter.Confirm(ctx, platform.ConfirmRequest{
		PresentationKey: "explorer.detach", PresentationArgs: map[string]string{"name": name, "path": path},
		Title:       c.uiText("explorer.detach.title", "Odłącz folder od FileES"),
		Text:        name + "\n" + path + "\n\n" + c.uiText("explorer.detach.text", "Synchronizacja zostanie zatrzymana. Wszystkie pliki pozostaną na dysku, także nieopublikowane zmiany. Repozytorium i historia na serwerze pozostaną bez zmian. Usunięte zostaną tylko lokalne metadane .svn i .filees oraz ikona FileES."),
		ConfirmText: c.uiText("explorer.detach.confirm", "Odłącz folder"), CancelText: c.uiText("action.cancel", "Anuluj"),
	})
	return err == nil && confirmed
}
