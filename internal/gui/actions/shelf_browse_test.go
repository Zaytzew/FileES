package actions_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
)

type recordingShelf struct {
	mu        sync.Mutex
	serverIDs []string
	channels  []string
	list      actions.ShelfList
	err       error
}

func (r *recordingShelf) ListShelf(_ context.Context, serverID, channelID string) (actions.ShelfList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.serverIDs = append(r.serverIDs, serverID)
	r.channels = append(r.channels, channelID)
	if r.err != nil {
		return actions.ShelfList{}, r.err
	}
	return r.list, nil
}

func (r *recordingShelf) asked() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.serverIDs...), append([]string(nil), r.channels...)
}

// listedShelf drives the channel dialog to browse once and then close, which is
// how an owner reaches a shelf: through the channel that names it.
func listedShelf(t *testing.T) *platformtest.Fake {
	t.Helper()
	dialogs := 0
	fake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogUploadChannels, ServerID: "office", RepoID: "repo-1"}, nil
		},
		UploadChannelsFunc: func(context.Context, platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
			dialogs++
			if dialogs == 1 {
				return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogBrowse, ChannelID: "channel-1"}, nil
			}
			return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
		},
	}
	return fake
}

// oneChannel is the channel manager with a single shelf on it. It is its own
// type rather than an embedding of recordingUploadChannels so the mutex in
// that one is never copied by a value receiver.
type oneChannel struct{}

func (oneChannel) ListUploadChannels(context.Context, string, string) (actions.UploadChannelList, error) {
	return actions.UploadChannelList{Channels: []actions.UploadChannelSummary{{
		ChannelID: "channel-1", Slug: "oferta-a", State: "active",
	}}}, nil
}

func (oneChannel) CreateUploadChannel(context.Context, string, actions.UploadChannelDeclaration) error {
	return nil
}

func (oneChannel) UpdateUploadChannel(context.Context, string, string, actions.UploadChannelDeclaration) error {
	return nil
}

func (oneChannel) RevokeUploadChannel(context.Context, string, string, string) error { return nil }

func (oneChannel) DeleteUploadChannel(context.Context, string, string, string) error { return nil }

func TestControllerBrowsesOneShelfThroughItsChannel(t *testing.T) {
	shelf := &recordingShelf{list: actions.ShelfList{ChannelID: "channel-1", Items: []actions.ShelfItem{{
		UploadID: "upload-1", RepoPath: "Opinia-Lodz.pdf", OriginalName: "Opinia Łódź.pdf",
		Size: 2048, Revision: 42, AcceptedAt: "2026-09-12T10:00:00Z",
	}}}}
	fake := listedShelf(t)
	intents, cancel := setup(actions.Config{
		ViewModel:       viewCopy(lifecycleView(uploadChannelCaps()...)),
		SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake,
		UploadChannels: oneChannel{}, Shelf: shelf, ShelfBrowser: fake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	for len(fake.Snapshot().ShelfRequests) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := fake.Snapshot().ShelfRequests
	if len(requests) != 1 {
		t.Fatalf("shelf requests=%d", len(requests))
	}
	request := requests[0]
	if request.ChannelID != "channel-1" || request.ShelfName != "oferta-a" {
		t.Fatalf("request=%+v", request)
	}
	if len(request.Items) != 1 {
		t.Fatalf("items=%+v", request.Items)
	}
	item := request.Items[0]
	// Both names travel: the one the contributor gave, and the one a fetch
	// will ask the repository for after the naming policy had its say.
	if item.OriginalName != "Opinia Łódź.pdf" || item.RepoPath != "Opinia-Lodz.pdf" {
		t.Fatalf("item=%+v", item)
	}
	if item.Revision != 42 || item.Size != 2048 {
		t.Fatalf("item=%+v", item)
	}
	servers, channels := shelf.asked()
	if len(channels) != 1 || channels[0] != "channel-1" || servers[0] != "office" {
		t.Fatalf("asked servers=%v channels=%v", servers, channels)
	}
}

// A shelf that cannot be read must not open an empty browser pretending the
// shelf is empty: those are different facts and the owner acts on them
// differently.
func TestControllerDoesNotOpenAShelfItCouldNotRead(t *testing.T) {
	shelf := &recordingShelf{err: errors.New("daemon unreachable")}
	fake := listedShelf(t)
	intents, cancel := setup(actions.Config{
		ViewModel:       viewCopy(lifecycleView(uploadChannelCaps()...)),
		SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake,
		UploadChannels: oneChannel{}, Shelf: shelf, ShelfBrowser: fake,
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(time.Second)
	asked := func() int { _, channels := shelf.asked(); return len(channels) }
	for asked() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if requests := fake.Snapshot().ShelfRequests; len(requests) != 0 {
		t.Fatalf("a browser opened for a shelf that could not be read: %+v", requests)
	}
}

// Without a lister or a browser wired the controller stays silent rather than
// opening a window it cannot fill.
func TestControllerSkipsBrowsingWhenTheShelfIsNotWired(t *testing.T) {
	fake := listedShelf(t)
	intents, cancel := setup(actions.Config{
		ViewModel:       viewCopy(lifecycleView(uploadChannelCaps()...)),
		SettingsBrowser: fake, UploadChannelBrowser: fake, Prompter: fake, Notifier: fake,
		UploadChannels: oneChannel{},
	})
	defer cancel()

	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	time.Sleep(100 * time.Millisecond)
	if requests := fake.Snapshot().ShelfRequests; len(requests) != 0 {
		t.Fatalf("shelf requests=%+v", requests)
	}
}
