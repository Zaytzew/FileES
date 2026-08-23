package main

import (
	"context"
	"errors"

	"filees/internal/gui/actions"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

type actionRunner interface {
	Run(context.Context)
}

// configureActions deliberately wires only the actions exposed by the first
// Wails UX slice.  The controller remains the authority on eligibility; the
// WebView projection merely avoids offering an obviously unavailable button.
func configureActions(service *GUIService, locker actions.LockUnlocker, reservations actions.ReservationManager, stack actions.StackLifecycle, backend platform.Backend, restart, shutdown func()) actionRunner {
	if backend == nil {
		return nil
	}
	intents := make(chan tray.Intent, 32)
	service.attachActions(intents)
	return actions.New(actions.Config{
		Intents:      intents,
		ViewModel:    service.viewModel,
		Opener:       backend,
		Picker:       backend,
		Prompter:     backend,
		Notifier:     actionNotifier{service: service},
		Locker:       locker,
		Reservations: reservations,
		Stack:        stack,
		Reconnect:    service.runner.Reconnect,
		Refresh:      service.runner.Refresh,
		Restart:      restart,
		Shutdown:     shutdown,
	})
}

type systemLifecycleClient interface {
	SystemRestart(context.Context) (*contract.SystemLifecycleResult, error)
	SystemShutdown(context.Context) (*contract.SystemLifecycleResult, error)
}

type stackLifecycleAdapter struct {
	client systemLifecycleClient
}

func (adapter stackLifecycleAdapter) RestartFileES(ctx context.Context) error {
	result, err := adapter.client.SystemRestart(ctx)
	return validateSystemLifecycleResult(result, "restart", err)
}

func (adapter stackLifecycleAdapter) ShutdownFileES(ctx context.Context) error {
	result, err := adapter.client.SystemShutdown(ctx)
	return validateSystemLifecycleResult(result, "shutdown", err)
}

func validateSystemLifecycleResult(result *contract.SystemLifecycleResult, expected string, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty lifecycle result")
	}
	if result.Action != expected {
		return errors.New("daemon returned an unexpected lifecycle action")
	}
	return nil
}

type reservationClient interface {
	RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error)
	RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error
}

type reservationAdapter struct {
	client reservationClient
}

func (adapter reservationAdapter) ListReservations(ctx context.Context, serverID string) ([]guiapp.Reservation, error) {
	result, err := adapter.client.RepoReservationList(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty reservation list")
	}
	reservations := make([]guiapp.Reservation, len(result.Reservations))
	for i, reservation := range result.Reservations {
		reservations[i] = guiapp.Reservation{
			ServerID: serverID, RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy,
			Path: reservation.Path, Token: reservation.Token, OwnerLabel: reservation.OwnerLabel,
			CreatedAt: reservation.CreatedAt, CanRelease: reservation.CanRelease,
			LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport,
		}
	}
	return reservations, nil
}

func (adapter reservationAdapter) ReleaseReservation(ctx context.Context, request guiapp.ReservationReleaseRequest) error {
	return adapter.client.RepoReservationRelease(ctx, contract.RepoReservationReleasePayload{
		ServerID: request.ServerID, RepoID: request.RepoID, Path: request.Path,
		ExpectedToken: request.ExpectedToken, ConfirmRisk: request.ConfirmRisk,
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
