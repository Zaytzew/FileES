package main

import (
	"context"

	"filees/internal/gui/actions"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
)

type actionRunner interface {
	Run(context.Context)
}

// configureActions deliberately wires only the actions exposed by the first
// Wails UX slice.  The controller remains the authority on eligibility; the
// WebView projection merely avoids offering an obviously unavailable button.
func configureActions(service *GUIService, locker actions.LockUnlocker, backend platform.Backend) actionRunner {
	if backend == nil {
		return nil
	}
	intents := make(chan tray.Intent, 32)
	service.attachActions(intents)
	return actions.New(actions.Config{
		Intents:   intents,
		ViewModel: service.viewModel,
		Opener:    backend,
		Picker:    backend,
		Notifier:  actionNotifier{service: service},
		Locker:    locker,
		Reconnect: service.runner.Reconnect,
		Refresh:   service.runner.Refresh,
	})
}

// actionNotifier keeps action feedback in the same Wails surface. It carries
// information only: no callback can turn a toast into a privileged action.
type actionNotifier struct {
	service *GUIService
}

func (notifier actionNotifier) Notify(_ context.Context, notification platform.Notification) error {
	notifier.service.emitActionFeedback(ActionFeedback{
		Level:   string(notification.Urgency),
		Title:   notification.Title,
		Message: notification.Body,
	})
	return nil
}
