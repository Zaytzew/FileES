package actions_test

import (
	"context"
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
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
