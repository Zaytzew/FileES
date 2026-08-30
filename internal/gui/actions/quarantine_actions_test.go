package actions_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type recordingQuarantine struct {
	mu      sync.Mutex
	list    actions.QuarantineList
	hidden  []string
	fetched []string
	payload actions.QuarantineFetch
}

func (r *recordingQuarantine) ListQuarantine(context.Context, string) (actions.QuarantineList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.list
	out.Items = append([]actions.QuarantineItem(nil), r.list.Items...)
	return out, nil
}
func (r *recordingQuarantine) HideQuarantine(_ context.Context, _, uploadID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hidden = append(r.hidden, uploadID)
	kept := r.list.Items[:0]
	for _, item := range r.list.Items {
		if item.UploadID != uploadID {
			kept = append(kept, item)
		}
	}
	r.list.Items = kept
	return nil
}
func (r *recordingQuarantine) FetchQuarantine(_ context.Context, _, uploadID string) (actions.QuarantineFetch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetched = append(r.fetched, uploadID)
	return r.payload, nil
}

func quarantineCaps() []string {
	return []string{
		contract.CapRepoUploadChannelList, contract.CapRepoUploadChannelCreate, contract.CapRepoUploadChannelUpdate,
		contract.CapRepoUploadChannelRevoke, contract.CapRepoUploadChannelDelete,
		contract.CapRepoQuarantineList, contract.CapRepoQuarantineHide, contract.CapRepoQuarantineFetch,
	}
}

func quarantineView() app.ViewModel {
	view := lifecycleView(quarantineCaps()...)
	trash := app.RepoViewModel{
		ID: "trash-1", ServerID: "office", DisplayName: "Kwarantanna",
		Attached: false, AttachmentPolicy: "optional", OwnerRealmID: "realm-1",
		Access: contract.AccessReadWrite, State: contract.StateActive, Purpose: "upload_trash",
	}
	view.Repos = append(view.Repos, trash)
	view.Servers[0].Repos = append(view.Servers[0].Repos, trash)
	return view
}

func TestControllerQuarantineHideIsSilentAndLeavesBytes(t *testing.T) {
	manager := &recordingQuarantine{list: actions.QuarantineList{Items: []actions.QuarantineItem{{
		UploadID: "u1", OriginalName: "wirus.exe", Size: 12, RemainingHours: 40, AVVerdict: "Eicar",
	}}}}
	var dialogs atomic.Int32
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogQuarantine, ServerID: "office", RepoID: "trash-1"}, nil
		},
		QuarantineFunc: func(context.Context, platform.QuarantineDialogRequest) (platform.QuarantineDialogResult, error) {
			if dialogs.Add(1) == 1 {
				return platform.QuarantineDialogResult{Action: platform.QuarantineDialogHide, UploadID: "u1"}, nil
			}
			return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, nil
		},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(quarantineView()), SettingsBrowser: platformFake, QuarantineBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, Quarantine: manager, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		hidden := len(manager.hidden)
		manager.mu.Unlock()
		if hidden == 1 && dialogs.Load() >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.hidden) != 1 || manager.hidden[0] != "u1" || len(manager.fetched) != 0 {
		t.Fatalf("hidden=%v fetched=%v", manager.hidden, manager.fetched)
	}
}

func TestControllerQuarantineFetchWritesCopyAndReportsRemainingHours(t *testing.T) {
	dir := t.TempDir()
	manager := &recordingQuarantine{
		list: actions.QuarantineList{Items: []actions.QuarantineItem{{
			UploadID: "u1", OriginalName: "Opinia.pdf", Size: 4, RemainingHours: 45,
		}}},
		payload: actions.QuarantineFetch{UploadID: "u1", OriginalName: "Opinia.pdf", Payload: []byte("plik"), RemainingHours: 45},
	}
	dialogs := 0
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogQuarantine, ServerID: "office", RepoID: "trash-1"}, nil
		},
		QuarantineFunc: func(context.Context, platform.QuarantineDialogRequest) (platform.QuarantineDialogResult, error) {
			dialogs++
			if dialogs == 1 {
				return platform.QuarantineDialogResult{Action: platform.QuarantineDialogFetch, UploadID: "u1"}, nil
			}
			return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, nil
		},
		PickFolderFunc: func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error) {
			return platform.PickFolderResult{Path: dir}, nil
		},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(quarantineView()), SettingsBrowser: platformFake, QuarantineBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, Quarantine: manager, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		for _, n := range platformFake.Snapshot().Notifications {
			if strings.Contains(n.Body, "45 godzin") {
				body = n.Body
			}
		}
		if body != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Opinia.pdf"))
	if err != nil || string(raw) != "plik" {
		t.Fatalf("saved=%q err=%v", raw, err)
	}
	if !strings.Contains(body, "Opinia.pdf") || !strings.Contains(body, "45 godzin") {
		t.Fatalf("remaining-hours notice=%q", body)
	}
}

func TestControllerDirectQuarantineIntentCloseIsSilent(t *testing.T) {
	manager := &recordingQuarantine{list: actions.QuarantineList{Items: []actions.QuarantineItem{{UploadID: "u1", OriginalName: "a.bin", RemainingHours: 10}}}}
	platformFake := &platformtest.Fake{
		QuarantineFunc: func(context.Context, platform.QuarantineDialogRequest) (platform.QuarantineDialogResult, error) {
			return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, nil
		},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(quarantineView()), QuarantineBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, Quarantine: manager, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentReviewQuarantine, ServerID: "office", RepoID: "trash-1"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(platformFake.Snapshot().QuarantineRequests) == 0 {
		time.Sleep(time.Millisecond)
	}
	snapshot := platformFake.Snapshot()
	if len(snapshot.QuarantineRequests) == 0 {
		t.Fatal("quarantine window was not shown")
	}
	if !snapshot.QuarantineRequests[0].DirectEntry {
		t.Fatal("direct quarantine intent was not marked as direct entry")
	}
	time.Sleep(30 * time.Millisecond)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.hidden) != 0 || len(manager.fetched) != 0 {
		t.Fatalf("silent close mutated waiting room: hidden=%v fetched=%v", manager.hidden, manager.fetched)
	}
}

func TestControllerAnnouncesPurgedQuarantineTTL(t *testing.T) {
	manager := &recordingQuarantine{list: actions.QuarantineList{Message: "Usunięto z kwarantanny 1 plik po 48 godzinach."}}
	platformFake := &platformtest.Fake{
		SettingsFunc: func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
			return platform.SettingsDialogResult{Action: platform.SettingsDialogQuarantine, ServerID: "office", RepoID: "trash-1"}, nil
		},
	}
	intents, cancel := setup(actions.Config{
		ViewModel: viewCopy(quarantineView()), SettingsBrowser: platformFake, QuarantineBrowser: platformFake,
		FolderPicker: platformFake, Prompter: platformFake, Quarantine: manager, Notifier: platformFake,
	})
	defer cancel()
	send(t, intents, tray.Intent{Kind: tray.IntentSettings, ServerID: "office"})
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, n := range platformFake.Snapshot().Notifications {
			if strings.Contains(n.Body, "48 godzinach") {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatalf("missing TTL komunikat: %+v", platformFake.Snapshot().Notifications)
	}
}
