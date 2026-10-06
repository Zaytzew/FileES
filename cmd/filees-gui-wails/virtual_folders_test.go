package main

import (
	"context"
	"errors"
	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
)

type virtualFolderGUIProbe struct {
	calls int
	err   error
	wrong bool
}

func TestUnsupportedVirtualFolderSettingsStayHidden(t *testing.T) {
	html := embeddedFrontendFile(t, "frontend/settings.html")
	css := embeddedFrontendFile(t, "frontend/settings.css")
	js := embeddedFrontendFile(t, "frontend/settings.js")
	if !strings.Contains(html, `id="virtual-folders-card" class="setting-card" hidden`) || !strings.Contains(css, "[hidden] { display: none !important; }") || !strings.Contains(js, `$("#virtual-folders-card").hidden = !snapshot.virtual_folders`) {
		t.Fatal("unsupported setting is not reliably hidden")
	}
}

func (p *virtualFolderGUIProbe) VirtualFolders(_ context.Context, enabled *bool) (*contract.VirtualFoldersStatus, error) {
	p.calls++
	return &contract.VirtualFoldersStatus{Enabled: *enabled != p.wrong}, p.err
}

func TestVirtualFoldersSettingsRejectsStaleAndUnscopedChanges(t *testing.T) {
	for _, variant := range []string{"enable", "inactive", "revision", "stale", "offline", "unsupported", "display", "busy", "lost-reply", "wrong-reply"} {
		t.Run(variant, func(t *testing.T) {
			d := &virtualFolderGUIProbe{}
			gui := &GUIService{view: guiapp.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapVirtualFolders: true}, VirtualFolders: &contract.VirtualFoldersStatus{}}}
			s := newSettingsService()
			s.gui = gui
			s.virtualClient = d
			s.active = &settingsSession{}
			s.snapshot.Revision = 7
			s.snapshot.VirtualFolders = &contract.VirtualFoldersStatus{}
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
			}
			result := s.VirtualFolders(true, rev)
			if result.Accepted != (variant == "enable") {
				t.Fatal(result)
			}
			want := 0
			if variant == "enable" || variant == "lost-reply" || variant == "wrong-reply" {
				want = 1
			}
			if d.calls != want {
				t.Fatal("unexpected mutation", d.calls)
			}
			if variant != "enable" && s.snapshot.VirtualFolders.Enabled {
				t.Fatal("unconfirmed preference published")
			}
		})
	}
}
