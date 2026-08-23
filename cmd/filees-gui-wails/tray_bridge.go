package main

import (
	"fmt"

	guiapp "filees/internal/gui/app"
	guitray "filees/internal/gui/tray"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type wailsTrayProjection struct {
	Icon    guiapp.IconState
	Status  string
	Tooltip string
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
	return wailsTrayProjection{Icon: icon, Status: status, Tooltip: "FileES — " + status}
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
	menu.Add("Zakończ renderer").OnClick(func(_ *application.Context) { host.Quit() })
	systemTray.SetMenu(menu)
	systemTray.OnClick(showWindow)

	service.attachSnapshotObserver(func(snapshot Snapshot) {
		projection := projectWailsTray(snapshot)
		statusItem.SetLabel(projection.Status)
		systemTray.SetTooltip(projection.Tooltip)
		if icon := icons[projection.Icon]; len(icon) > 0 {
			systemTray.SetIcon(icon)
		}
	})
}
