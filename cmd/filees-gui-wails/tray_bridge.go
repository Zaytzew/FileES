package main

import (
	"fmt"
	"log"
	"sync"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	guitray "filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const openAnnouncementEvent = "filees:open-announcement"
const nativeLanguageEvent = "filees:native-language"

type wailsTrayProjection struct {
	Icon        guiapp.IconState
	Status      string
	Tooltip     string
	CanRestart  bool
	CanShutdown bool
	Unread      int
}

func projectWailsTray(snapshot Snapshot, locales ...nativeLanguage) wailsTrayProjection {
	language := nativePresentationLanguage(locales)
	icon := guiapp.IconState(snapshot.IconState)
	if icon == "" {
		icon = guiapp.IconDisconnected
	}
	state := language.text("tray.status.disconnected")
	if snapshot.Connected && snapshot.Stale {
		state = language.text("tray.status.refreshing")
	} else if snapshot.Connected {
		state = language.text("tray.status.connected")
	}
	locks := len(snapshot.Reservations)
	lockStatus := language.format("tray.status.locks", map[string]string{"count": fmt.Sprint(locks)})
	reservationStatus := snapshot.ReservationStatus
	if reservationStatus.State == "partial" {
		lockStatus = language.format("tray.status.partial", map[string]string{"count": fmt.Sprint(locks), "servers": fmt.Sprint(len(reservationStatus.Unavailable))})
	} else if len(reservationStatus.Offline) > 0 {
		lockStatus = language.format("tray.status.mirror", map[string]string{"locks": lockStatus, "servers": fmt.Sprint(len(reservationStatus.Offline))})
	} else if len(reservationStatus.Stale) > 0 {
		lockStatus = language.format("tray.status.stale", map[string]string{"locks": lockStatus, "servers": fmt.Sprint(len(reservationStatus.Stale))})
	} else if reservationStatus.State == "daemon_offline" || !snapshot.Connected {
		lockStatus = language.format("tray.status.unverified", map[string]string{"locks": lockStatus})
	}
	repositories := len(snapshot.Repositories)
	status := language.format("tray.status.summary", map[string]string{"state": state, "count": fmt.Sprint(repositories), "locks": lockStatus})
	unread := 0
	for _, notice := range snapshot.Notices {
		if !notice.Acked {
			unread++
		}
	}
	if unread > 0 {
		status = language.format("tray.status.unread", map[string]string{"count": fmt.Sprint(unread), "status": status})
	}
	return wailsTrayProjection{
		Icon: icon, Status: status, Tooltip: trayTooltip(snapshot, status, language),
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

func repositoryNoun(count int) string {
	if count == 1 {
		return "repozytorium"
	}
	lastTwo := count % 100
	last := count % 10
	if (lastTwo < 12 || lastTwo > 14) && last >= 2 && last <= 4 {
		return "repozytoria"
	}
	return "repozytoriów"
}

// intentAlertPolicy keeps a notification per unresolved episode. Stale and
// disconnected snapshots never clear an episode. Unlike historical shouts,
// unresolved work deserves a reminder on the first fresh snapshot after start.
type intentAlertPolicy struct {
	active map[string]bool
}

func (policy *intentAlertPolicy) Observe(snapshot Snapshot, locales ...nativeLanguage) []platform.Notification {
	language := nativePresentationLanguage(locales)
	if !snapshot.Connected || snapshot.Stale {
		return nil
	}
	next := make(map[string]bool)
	var result []platform.Notification
	for _, repo := range snapshot.Repositories {
		if !repo.IntentResolutionRequired {
			continue
		}
		key := repo.ServerID + ":" + repo.ID
		next[key] = true
		if policy.active[key] {
			continue
		}
		name := repo.DisplayName
		if name == "" {
			name = repo.ID
		}
		result = append(result, platform.Notification{
			ID: "intent." + key, Group: "intent." + key,
			Title:   language.text("notification.intent.title"),
			Body:    language.format("notification.intent.body", map[string]string{"name": name}),
			Urgency: platform.UrgencyCritical,
		})
	}
	policy.active = next
	return result
}

type announcementAlertPolicy struct {
	initialized bool
	seen        map[string]struct{}
}

func (policy *announcementAlertPolicy) Observe(snapshot Snapshot, locales ...nativeLanguage) []platform.Notification {
	language := nativePresentationLanguage(locales)
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
			Title: language.text("notification.announcement.title"), Body: body, Urgency: platform.UrgencyCritical,
		})
	}
	return result
}

func configureWailsTray(host *application.App, window *application.WebviewWindow, service *GUIService, notifier platform.Notifier) {
	language, err := loadNativeLanguage()
	if err != nil {
		log.Printf("native GUI catalogue unavailable: %v", err)
	}
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
	statusItem := menu.Add(language.text("tray.starting")).SetEnabled(false)
	menu.AddSeparator()
	showItem := menu.Add(language.text("tray.show")).OnClick(func(_ *application.Context) { showWindow() })
	announcementItem := menu.Add(language.text("tray.announcements")).OnClick(func(_ *application.Context) {
		showWindow()
		host.Event.Emit(openAnnouncementEvent)
	})
	announcementItem.SetHidden(true)
	refreshItem := menu.Add(language.text("tray.refresh")).OnClick(func(_ *application.Context) { service.Refresh() })
	activateItem := menu.Add(language.text("tray.activate")).OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentActivate)})
	})
	menu.AddSeparator()
	fileESMenu := menu.AddSubmenu("FileES")
	restartItem := fileESMenu.Add(language.text("tray.restart")).OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentRestartFileES)})
	})
	shutdownItem := fileESMenu.Add(language.text("tray.quit")).OnClick(func(_ *application.Context) {
		service.Trigger(ActionRequest{Kind: string(guitray.IntentShutdownFileES)})
	})
	systemTray.SetMenu(menu)
	systemTray.OnClick(showWindow)

	var alerts announcementAlertPolicy
	var intentAlerts intentAlertPolicy
	var trayMu sync.Mutex
	var lastRevision uint64
	var lastSnapshot Snapshot
	unread := 0
	refreshMenuLabels := func() {
		showItem.SetLabel(language.text("tray.show"))
		refreshItem.SetLabel(language.text("tray.refresh"))
		activateItem.SetLabel(language.text("tray.activate"))
		restartItem.SetLabel(language.text("tray.restart"))
		shutdownItem.SetLabel(language.text("tray.quit"))
		label := language.text("tray.announcements")
		if unread > 0 {
			label = fmt.Sprintf("%s (%d)", label, unread)
		}
		announcementItem.SetLabel(label)
		if lastRevision == 0 {
			statusItem.SetLabel(language.text("tray.starting"))
		}
	}
	// Presentation-only path: never Observe(), Notify(), Refresh() or Trigger().
	// Only the main window resolves the preference for the native menu.
	host.Event.On(nativeLanguageEvent, func(event *application.CustomEvent) {
		if event.Sender != "filees-main" {
			return
		}
		locale, ok := event.Data.(string)
		if !ok {
			return
		}
		trayMu.Lock()
		defer trayMu.Unlock()
		if language.selectLocale(locale) {
			refreshMenuLabels()
			if lastRevision > 0 {
				projection := projectWailsTray(lastSnapshot, language)
				statusItem.SetLabel(projection.Status)
				systemTray.SetTooltip(projection.Tooltip)
				if err := publishNativeTrayTooltip(projection.Tooltip); err != nil {
					log.Printf("localized tray tooltip pending registration: %v", err)
				}
			}
		}
	})
	lastTooltip := ""
	tooltipFailureLogged := false
	service.attachSnapshotObserver(func(snapshot Snapshot) {
		trayMu.Lock()
		defer trayMu.Unlock()
		if snapshot.Revision < lastRevision {
			return
		}
		lastRevision = snapshot.Revision
		lastSnapshot = snapshot
		projection := projectWailsTray(snapshot, language)
		statusItem.SetLabel(projection.Status)
		announcementItem.SetHidden(projection.Unread == 0)
		unread = projection.Unread
		refreshMenuLabels()
		restartItem.SetHidden(!projection.CanRestart)
		shutdownItem.SetHidden(!projection.CanShutdown)
		systemTray.SetTooltip(projection.Tooltip)
		if projection.Tooltip != lastTooltip {
			if err := publishNativeTrayTooltip(projection.Tooltip); err == nil {
				lastTooltip = projection.Tooltip
				tooltipFailureLogged = false
			} else if !tooltipFailureLogged {
				log.Printf("tray tooltip is pending native registration: %v", err)
				tooltipFailureLogged = true
			}
		}
		if icon := icons[projection.Icon]; len(icon) > 0 {
			systemTray.SetIcon(icon)
		}
		if notifier != nil {
			for _, notification := range append(alerts.Observe(snapshot, language), intentAlerts.Observe(snapshot, language)...) {
				notification := notification
				go func() {
					if err := notifier.Notify(host.Context(), notification); err != nil {
						log.Printf("desktop notification failed: %v", err)
					}
				}()
			}
		}
	})
}
