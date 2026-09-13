package actions_test

import (
	"context"
	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type downloadingShelf struct {
	recordingShelf
	localPath string
	started   atomic.Int32
}

func (s *downloadingShelf) ShelfFetch(_ context.Context, server, repo, channel, upload, path string, inspect bool) (actions.ShelfDownload, error) {
	if inspect {
		return actions.ShelfDownload{LocalPath: s.localPath}, nil
	}
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
				if request.Title != "Wybierz folder na przyjęte pliki półki „oferta-a”" {
					t.Errorf("unformatted shelf picker title: %q", request.Title)
				}
				picks.Add(1)
				return platform.PickFolderResult{Path: filepath.Join(t.TempDir(), "shelf")}, nil
			}
			fake.ShelfFunc = func(context.Context, platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
				return platform.ShelfDialogResult{Action: platform.ShelfDialogFetch, UploadID: "upload"}, nil
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
			if len(fake.Snapshot().Notifications) < 2 {
				t.Fatal("missing pending/completed presentation")
			}
		})
	}
}
