package actions_test

import (
	"context"
	"errors"
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type downloadingShelf struct {
	recordingShelf
	localPath string
	started   atomic.Int32
	requested atomic.Value
}

func (s *downloadingShelf) ShelfFetch(_ context.Context, server, repo, channel, upload, path string, inspect bool) (actions.ShelfDownload, error) {
	if inspect {
		return actions.ShelfDownload{LocalPath: s.localPath}, nil
	}
	s.requested.Store(path)
	s.started.Add(1)
	return actions.ShelfDownload{OperationID: "op", FetchID: "fetch", UploadID: upload, LocalPath: path, State: "queued"}, nil
}
func (s *downloadingShelf) ShelfFetchStatus(context.Context, string) (actions.ShelfDownload, error) {
	return actions.ShelfDownload{OperationID: "op", FetchID: "fetch", State: "complete"}, nil
}
func TestShelfGUISelectionUsesFolderOnlyAtFirstDownload(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "attached"}[attached], func(t *testing.T) {
			shelf := &downloadingShelf{}
			shelf.list = actions.ShelfList{ChannelID: "channel-1", Items: []actions.ShelfItem{{UploadID: "upload"}}}
			if attached {
				shelf.localPath = filepath.Join(t.TempDir(), "shelf")
			}
			fake := listedShelf(t)
			var picks atomic.Int32
			fake.PickFolderFunc = func(_ context.Context, request platform.PickFolderRequest) (platform.PickFolderResult, error) {
				if request.Title != "Wybierz miejsce na nowy folder półki" {
					t.Errorf("unformatted shelf picker title: %q", request.Title)
				}
				picks.Add(1)
				return platform.PickFolderResult{Path: filepath.Join(t.TempDir(), "shelf")}, nil
			}
			fake.ShelfFunc = func(context.Context, platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
				return platform.ShelfDialogResult{Action: platform.ShelfDialogFetch, UploadID: "upload"}, nil
			}
			fake.PromptTextFunc = func(_ context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
				if request.Default != "oferta-a" {
					t.Errorf("name default=%q", request.Default)
				}
				return platform.PromptTextResult{Value: "my-shelf"}, nil
			}
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView(uploadChannelCaps()...)), SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake, FolderPicker: fake, UploadChannels: oneChannel{}, Shelf: shelf, ShelfBrowser: fake})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
			deadline := time.Now().Add(4 * time.Second)
			for len(fake.Snapshot().Notifications) < 2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if shelf.started.Load() != 1 {
				t.Fatal("download did not start")
			}
			want := int32(1)
			if attached {
				want = 0
			}
			if picks.Load() != want {
				t.Fatalf("folder picks=%d want=%d", picks.Load(), want)
			}
			if !attached {
				path := shelf.requested.Load().(string)
				if filepath.Base(path) != "my-shelf" {
					t.Errorf("not a named child folder: %q", path)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("GUI created a folder instead of asking daemon: %v", err)
				}
			}
			if len(fake.Snapshot().Notifications) < 2 {
				t.Fatal("missing pending/completed presentation")
			}
		})
	}
}

func TestShelfFolderCreationCancellationDoesNotStartDownload(t *testing.T) {
	shelf := &downloadingShelf{}
	shelf.list = actions.ShelfList{ChannelID: "channel-1", Items: []actions.ShelfItem{{UploadID: "upload"}}}
	fake := listedShelf(t)
	prompted := make(chan struct{}, 1)
	fake.PickFolderFunc = func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
		return platform.PickFolderResult{Path: t.TempDir()}, nil
	}
	fake.PromptTextFunc = func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
		prompted <- struct{}{}
		return platform.PromptTextResult{Cancelled: true}, nil
	}
	fake.ShelfFunc = func(context.Context, platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
		return platform.ShelfDialogResult{Action: platform.ShelfDialogFetch, UploadID: "upload"}, nil
	}
	intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView(uploadChannelCaps()...)), SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake, FolderPicker: fake, UploadChannels: oneChannel{}, Shelf: shelf, ShelfBrowser: fake})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	select {
	case <-prompted:
	case <-time.After(time.Second):
		t.Fatal("name dialog missing")
	}
	time.Sleep(50 * time.Millisecond)
	if shelf.started.Load() != 0 {
		t.Fatal("cancelled creation started a download")
	}
}

type automaticImportShelf struct {
	downloadingShelf
	parent  string
	allowed bool
	cause   error
}

func (s *automaticImportShelf) ShelfFetch(ctx context.Context, server, repo, channel, upload, path string, inspect bool) (actions.ShelfDownload, error) {
	if inspect {
		return actions.ShelfDownload{LocalPath: s.localPath, ParentPath: s.parent, CanImport: s.allowed}, nil
	}
	return actions.ShelfDownload{}, errors.New("import must not become a standalone download")
}
func (s *automaticImportShelf) ShelfImport(_ context.Context, server, repo, channel, upload, path, destination string) (actions.ShelfDownload, error) {
	s.requested.Store([]string{path, destination})
	s.started.Add(1)
	if s.cause != nil {
		return actions.ShelfDownload{}, s.cause
	}
	return actions.ShelfDownload{OperationID: "op", FetchID: "fetch", State: "queued"}, nil
}
func TestShelfImportUsesParentAutomaticallyWithoutAnyFolderDialog(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		attached, denied, missingParent, collision bool
	}{
		{name: "first import"}, {name: "attached", attached: true}, {name: "collision", attached: true, collision: true},
		{name: "not authorized", denied: true}, {name: "missing parent", missingParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shelf := &automaticImportShelf{parent: t.TempDir(), allowed: !tc.denied}
			if tc.attached {
				shelf.localPath = filepath.Join(t.TempDir(), "shelf")
			}
			if tc.missingParent {
				shelf.parent = ""
			}
			if tc.collision {
				shelf.cause = &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "REPO-2020", MessageKey: "repo.rename_target_exists", Severity: "ERROR", Hint: "REQUIRE_ACTION"}}
			}
			shelf.list = actions.ShelfList{ChannelID: "channel-1", Items: []actions.ShelfItem{{UploadID: "upload"}}}
			fake := listedShelf(t)
			fake.PickFolderFunc = func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
				t.Error("unexpected folder dialog")
				return platform.PickFolderResult{Cancelled: true}, nil
			}
			fake.PromptTextFunc = func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error) {
				t.Error("unexpected cache name dialog")
				return platform.PromptTextResult{Cancelled: true}, nil
			}
			fake.ShelfFunc = func(context.Context, platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
				return platform.ShelfDialogResult{Action: platform.ShelfDialogImport, UploadID: "upload"}, nil
			}
			closed := make(chan struct{})
			base := fake.UploadChannelsFunc
			var views int
			fake.UploadChannelsFunc = func(ctx context.Context, r platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
				views++
				if views == 2 {
					close(closed)
				}
				return base(ctx, r)
			}
			intents, cancel := setup(actions.Config{ViewModel: viewCopy(lifecycleView(uploadChannelCaps()...)), SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake, UploadChannels: oneChannel{}, Shelf: shelf, ShelfBrowser: fake,
				// No FolderPicker is wired at all. Import must not depend on it.
				DomainText: func(_, key string, _ map[string]string) string {
					if key == "repo.rename_target_exists" {
						return "Obiekt o tej nazwie już istnieje w tym folderze"
					}
					return key
				},
			})
			defer cancel()
			send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
			select {
			case <-closed:
			case <-time.After(4 * time.Second):
				t.Fatal("import flow did not finish")
			}
			if tc.denied || tc.missingParent {
				if shelf.started.Load() != 0 {
					t.Fatal("unavailable import started")
				}
				return
			}
			if shelf.started.Load() != 1 {
				t.Fatal("automatic import did not start exactly once")
			}
			got := shelf.requested.Load().([]string)
			if got[0] != shelf.localPath || got[1] != shelf.parent {
				t.Fatalf("wrong import paths: %v", got)
			}
			if tc.collision {
				notices := fake.Snapshot().Notifications
				if len(notices) != 1 || notices[0].Body != "Obiekt o tej nazwie już istnieje w tym folderze" {
					t.Fatalf("collision not localized: %+v", notices)
				}
			}
		})
	}
}
