package main

import (
	"fmt"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	guitray "filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const openAnnouncementEvent = "filees:open-announcement"

type wailsTrayProjection struct {
	Icon        guiapp.IconState
	Status      string
	Tooltip     string
	CanRestart  bool
	CanShutdown bool
	Unread      int
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
	unread := 0
	for _, notice := range snapshot.Notices {
		if !notice.Acked {
			unread++
		}
	}
	if unread > 0 {
		status = fmt.Sprintf("%d %s do przejrzenia · %s", unread, announcementNoun(unread), status)
	}
	return wailsTrayProjection{
		Icon: icon, Status: status, Tooltip: "FileES — " + status,
		CanRestart:  snapshot.Connected && !snapshot.Stale && hasCapability(snapshot, contract.CapSystemRestart),
		CanShutdown: snapshot.Connected && !snapshot.Stale && hasCapability(snapshot, contract.CapSystemShutdown),
		Unread:      unread,
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

func announcementNoun(count int) string {
	if count == 1 {
		return "ogłoszenie"
	}
	lastTwo := count % 100
	last := count % 10
	if (lastTwo < 12 || lastTwo > 14) && last >= 2 && last <= 4 {
		return "ogłoszenia"
	}
	return "ogłoszeń"
}

type announcementAlertPolicy struct {
	initialized bool
	seen        map[string]struct{}
}

func (policy *announcementAlertPolicy) Observe(snapshot Snapshot) []platform.Notification {
	if !snapshot.Connected || snapshot.Stale {
		return nil
	}
	if policy.seen == nil {
		policy.seen = make(map[string]struct{})
	}
	if !policy.initialized {
		for _, notice := range snapshot.Notices {
			policy.seen[notice.ID] = struct{}{}
		}
		policy.initialized = true
		return nil
	}
	repositories := make(map[string]string, len(snapshot.Repositories))
	for _, repo := range snapshot.Repositories {
		name := repo.DisplayName
		if name == "" {
			name = repo.ID
		}
		repositories[repo.ID] = name
	}
	var result []platform.Notification
	for _, notice := range snapshot.Notices {
		if _, exists := policy.seen[notice.ID]; exists {
			continue
		}
		policy.seen[notice.ID] = struct{}{}
		if notice.Acked {
			continue
		}
		body := notice.Title
		if repository := repositories[notice.RepoID]; repository != "" {
			body = repository + " — " + body
		}
		result = append(result, platform.Notification{
			ID: "announcement." + notice.ID, Group: "announcement." + notice.ID,
			Title: "Nowe ogłoszenie", Body: body, Urgency: platform.UrgencyCritical,
		})
	}
	return result
}

func configureWailsTray(host *application.App, window *application.WebviewWindow, service *GUIService, notifier platform.Notifier) {
	systemTray := host.SystemTray.New()
	icons := guitray.WailsPlatformIcons()
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
	announcementItem := menu.Add("Otwórz ogłoszenia").OnClick(func(_ *application.Context) {
		showWindow()
		host.Event.Emit(openAnnouncementEvent)
	})
	announcementItem.SetHidden(true)
	menu.Add("Odśwież stan").OnClick(func(_ *application.Context) { service.Refresh() })
	menu.Add("Aktywuj klienta na nowym serwerze…").OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentActivate)})
	})
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

	var alerts announcementAlertPolicy
	service.attachSnapshotObserver(func(snapshot Snapshot) {
		projection := projectWailsTray(snapshot)
		statusItem.SetLabel(projection.Status)
		announcementItem.SetHidden(projection.Unread == 0)
		if projection.Unread > 0 {
			announcementItem.SetLabel(fmt.Sprintf("Otwórz ogłoszenia (%d)", projection.Unread))
		}
		restartItem.SetHidden(!projection.CanRestart)
		shutdownItem.SetHidden(!projection.CanShutdown)
		systemTray.SetTooltip(projection.Tooltip)
		if icon := icons[projection.Icon]; len(icon) > 0 {
			systemTray.SetIcon(icon)
		}
		if notifier != nil {
			for _, notification := range alerts.Observe(snapshot) {
				notification := notification
				go func() { _ = notifier.Notify(host.Context(), notification) }()
			}
		}
	})
}
