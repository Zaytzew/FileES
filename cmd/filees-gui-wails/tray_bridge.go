package main

import (
	"fmt"

	guiapp "filees/internal/gui/app"
	guitray "filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type wailsTrayProjection struct {
	Icon        guiapp.IconState
	Status      string
	Tooltip     string
	CanRestart  bool
	CanShutdown bool
}

func projectWailsTray(snapshot Snapshot) wailsTrayProjection {
	icon := guiapp.IconState(snapshot.IconState)
	if icon == "" {
		icon = guiapp.IconDisconnected
	}
	state := "Rozłączono"
	if snapshot.Connected && snapshot.Stale {
		state = "Odświeżanie"
	} else if snapshot.Connected {
		state = "Połączono"
	}
	locks := len(snapshot.Reservations)
	lockStatus := fmt.Sprintf("%d %s", locks, lockNoun(locks))
	if snapshot.Connected {
		for _, server := range snapshot.Servers {
			if !server.ReservationsKnown {
				lockStatus = "? blokad"
				break
			}
		}
	}
	status := fmt.Sprintf("%s · %d repo · %s", state, len(snapshot.Repositories), lockStatus)
	return wailsTrayProjection{
		Icon: icon, Status: status, Tooltip: "FileES — " + status,
		CanRestart:  snapshot.Connected && !snapshot.Stale && hasCapability(snapshot, contract.CapSystemRestart),
		CanShutdown: snapshot.Connected && !snapshot.Stale && hasCapability(snapshot, contract.CapSystemShutdown),
	}
}

func hasCapability(snapshot Snapshot, capability string) bool {
	for _, item := range snapshot.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

func lockNoun(count int) string {
	if count == 1 {
		return "blokada"
	}
	lastTwo := count % 100
	last := count % 10
	if lastTwo < 12 || lastTwo > 14 {
		if last >= 2 && last <= 4 {
			return "blokady"
		}
	}
	return "blokad"
}

func configureWailsTray(host *application.App, window *application.WebviewWindow, service *GUIService) {
	systemTray := host.SystemTray.New()
	icons := guitray.PlatformIcons()
	// Do not let the Wails fallback flash or persist while the first daemon
	// projection is still in flight. The projection will replace this with the
	// corresponding FileES status overlay as soon as it arrives.
	if icon := icons[guiapp.IconDisconnected]; len(icon) > 0 {
		host.SetIcon(icon)
		systemTray.SetIcon(icon)
	}

	showWindow := func() {
		window.Show()
		window.UnMinimise()
		window.Focus()
	}
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		window.Hide()
		event.Cancel()
	})

	menu := host.NewMenu()
	statusItem := menu.Add("FileES · uruchamianie").SetEnabled(false)
	menu.AddSeparator()
	menu.Add("Pokaż panel").OnClick(func(_ *application.Context) { showWindow() })
	menu.Add("Odśwież stan").OnClick(func(_ *application.Context) { service.Refresh() })
	menu.AddSeparator()
	fileESMenu := menu.AddSubmenu("FileES")
	restartItem := fileESMenu.Add("Uruchom FileES ponownie…").OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentRestartFileES)})
	})
	shutdownItem := fileESMenu.Add("Zakończ…").OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentShutdownFileES)})
	})
	systemTray.SetMenu(menu)
	systemTray.OnClick(showWindow)

	service.attachSnapshotObserver(func(snapshot Snapshot) {
		projection := projectWailsTray(snapshot)
		statusItem.SetLabel(projection.Status)
		restartItem.SetHidden(!projection.CanRestart)
		shutdownItem.SetHidden(!projection.CanShutdown)
		systemTray.SetTooltip(projection.Tooltip)
		if icon := icons[projection.Icon]; len(icon) > 0 {
			systemTray.SetIcon(icon)
		}
	})
}
