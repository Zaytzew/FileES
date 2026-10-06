package main

import (
	"context"
	"errors"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
)

type virtualFolderGUIProbe struct {
	calls       int
	err         error
	wrong       bool
	wrongServer bool
	serverID    string
}

func TestUnsupportedVirtualFolderSettingsStayHidden(t *testing.T) {
	html := embeddedFrontendFile(t, "frontend/settings.html")
	css := embeddedFrontendFile(t, "frontend/settings.css")
	js := embeddedFrontendFile(t, "frontend/settings.js")
	if !strings.Contains(html, `id="virtual-folders-card" class="setting-card" hidden`) || !strings.Contains(css, "[hidden] { display: none !important; }") || !strings.Contains(js, `$("#virtual-folders-card").hidden = !snapshot.virtual_folders`) {
		t.Fatal("unsupported setting is not reliably hidden")
	}
}

func (p *virtualFolderGUIProbe) VirtualFolders(_ context.Context, serverID string, enabled *bool) (*contract.VirtualFoldersStatus, error) {
	p.calls++
	p.serverID = serverID
	if p.wrongServer {
		serverID = "other"
	}
	return &contract.VirtualFoldersStatus{ServerID: serverID, Enabled: *enabled != p.wrong}, p.err
}

func TestVirtualFoldersSettingsRejectsStaleAndUnscopedChanges(t *testing.T) {
	for _, variant := range []string{"enable", "inactive", "revision", "stale", "offline", "unsupported", "display", "busy", "lost-reply", "wrong-reply", "wrong-server", "unknown-server"} {
		t.Run(variant, func(t *testing.T) {
			d := &virtualFolderGUIProbe{}
			gui := &GUIService{view: virtualFolderTestView()}
			s := newSettingsService()
			s.gui = gui
			s.virtualClient = d
			s.active = &settingsSession{}
			s.snapshot.Revision = 7
			s.snapshot.VirtualFolders = &contract.VirtualFoldersStatus{}
			s.snapshot.Server.ID = "selected"
			rev := uint64(7)
			switch variant {
			case "inactive":
				s.active = nil
			case "revision":
				rev++
			case "stale":
				gui.view.Stale = true
			case "offline":
				gui.view.Connected = false
			case "unsupported":
				delete(gui.view.Capabilities, contract.CapVirtualFolders)
			case "display":
				gui.recoveringDisplay = true
			case "busy":
				s.virtualBusy = true
			case "lost-reply":
				d.err = errors.New("lost")
			case "wrong-reply":
				d.wrong = true
			case "wrong-server":
				d.wrongServer = true
			case "unknown-server":
				s.snapshot.Server.ID = "unknown"
			}
			result := s.VirtualFolders(true, rev)
			if result.Accepted != (variant == "enable") {
				t.Fatal(result)
			}
			want := 0
			if variant == "enable" || variant == "lost-reply" || variant == "wrong-reply" || variant == "wrong-server" {
				want = 1
			}
			if d.calls != want {
				t.Fatal("unexpected mutation", d.calls)
			}
			if d.calls != 0 && d.serverID != "selected" {
				t.Fatal("wrong server called", d.serverID)
			}
			if variant != "enable" && s.snapshot.VirtualFolders.Enabled {
				t.Fatal("unconfirmed preference published")
			}
		})
	}
}

func TestDashboardVirtualFoldersSharesSettingWithoutOpeningDialog(t *testing.T) {
	for _, variant := range []string{"enable", "revision", "stale", "offline", "unsupported", "active-dialog", "busy", "display", "lost-reply", "wrong-reply", "wrong-server", "unknown-server", "already-enabled"} {
		t.Run(variant, func(t *testing.T) {
			d := &virtualFolderGUIProbe{}
			vm := virtualFolderTestView()
			gui := &GUIService{view: vm, snapshot: projectViewModel(vm, journal.Texts{})}
			gui.snapshot.Revision = 9
			s := newSettingsService()
			s.gui, s.virtualClient = gui, d
			rev := uint64(9)
			serverID := "selected"
			switch variant {
			case "revision":
				rev++
			case "stale":
				gui.view.Stale = true
			case "offline":
				gui.view.Connected = false
			case "unsupported":
				delete(vm.Capabilities, contract.CapVirtualFolders)
			case "active-dialog":
				s.active = &settingsSession{}
			case "busy":
				s.virtualBusy = true
			case "display":
				gui.recoveringDisplay = true
			case "lost-reply":
				d.err = errors.New("lost")
			case "wrong-reply":
				d.wrong = true
			case "wrong-server":
				d.wrongServer = true
			case "unknown-server":
				serverID = "unknown"
			case "already-enabled":
				gui.snapshot.Servers[0].VirtualFolders.Enabled = true
			}
			result := s.DashboardVirtualFolders(serverID, true, rev)
			if result.Accepted != (variant == "enable") {
				t.Fatal(result)
			}
			want := 0
			if variant == "enable" || variant == "lost-reply" || variant == "wrong-reply" || variant == "wrong-server" {
				want = 1
			}
			if d.calls != want {
				t.Fatal("unexpected daemon call or retry", d.calls)
			}
			if d.calls != 0 && d.serverID != "selected" {
				t.Fatal("wrong server called", d.serverID)
			}
		})
	}
}

func TestMainPanelVirtualFolderStateComesOnlyFromFreshDaemon(t *testing.T) {
	vm := virtualFolderTestView()
	projected := projectViewModel(vm, journal.Texts{})
	if projected.Servers[0].VirtualFolders == nil || projected.Servers[0].VirtualFolders.Enabled || !projected.Servers[1].VirtualFolders.Enabled {
		t.Fatal("missing preference")
	}
	vm.VirtualFolders.Servers["other"] = false
	if !projected.Servers[1].VirtualFolders.Enabled {
		t.Fatal("projection shares mutable state")
	}
	vm.Stale = true
	if projectViewModel(vm, journal.Texts{}).Servers[0].VirtualFolders != nil {
		t.Fatal("stale preference shown as control")
	}
}

func virtualFolderTestView() guiapp.ViewModel {
	return guiapp.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapVirtualFolders: true},
		Servers:        []guiapp.ServerViewModel{{ID: "selected"}, {ID: "other"}},
		VirtualFolders: &contract.VirtualFoldersStatus{Servers: map[string]bool{"other": true}}}
}

func TestAnchorCreationUsesOnlyItsServerPreference(t *testing.T) {
	vm := virtualFolderTestView()
	vm.Capabilities[contract.CapRepoAttachIntent] = true
	vm.Capabilities[contract.CapRepoAttachApprove] = true
	vm.Capabilities[contract.CapRepoExplorerAnchor] = true
	for _, serverID := range []string{"selected", "other"} {
		repo := guiapp.RepoViewModel{ID: "repo", ServerID: serverID, State: contract.StateUnattached}
		if canCreateAnchor(vm, repo) != (serverID == "other") {
			t.Fatal("another server's opt-in admitted creation", serverID)
		}
	}
	delete(vm.Capabilities, contract.CapVirtualFolders)
	if virtualFoldersForServer(vm, "other") != nil {
		t.Fatal("old daemon shown as per-server control")
	}
}
