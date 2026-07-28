package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/notifications"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localpin"
)

var errGUIRestartRequested = errors.New("GUI restart requested")

type dependencies struct {
	tray      tray.Backend
	platform  platform.Backend
	client    app.DaemonClient
	icons     tray.IconSet
	activator actions.Activator
	// pinStore is nil if the local PIN store could not be opened (e.g. no
	// durable state root available) - the local-PIN feature is then simply
	// disabled, activation and mobile pairing still work.
	pinStore *localpin.Store
}

type viewStore struct {
	mu sync.RWMutex
	vm app.ViewModel
}

type updateClient interface {
	UpdatePlan(context.Context) (*contract.UpdatePlanResult, error)
	UpdateApply(context.Context) (*contract.UpdateApplyResult, error)
}

type repositoryCreateClient interface {
	RepoCreateRequest(context.Context, contract.RepoCreateRequestPayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryCreateAdapter struct{ client repositoryCreateClient }

func (adapter repositoryCreateAdapter) CreateRepository(ctx context.Context, serverID, displayName, localPath string) (string, error) {
	result, err := adapter.client.RepoCreateRequest(ctx, contract.RepoCreateRequestPayload{ServerID: serverID, DisplayName: displayName, LocalPath: localPath})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", errors.New("daemon returned an empty repository operation")
	}
	return result.OperationID, nil
}

func (adapter repositoryCreateAdapter) CreationStatus(ctx context.Context, operationID string) (state, lastError string, err error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository operation")
	}
	return result.State, result.LastError, nil
}

type repositoryDetachClient interface {
	RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error)
	RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error)
}

type repositoryDetachAdapter struct{ client repositoryDetachClient }

type reservationClient interface {
	RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error)
	RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error
}

type reservationAdapter struct{ client reservationClient }

func (adapter reservationAdapter) ListReservations(ctx context.Context, serverID string) ([]app.Reservation, error) {
	result, err := adapter.client.RepoReservationList(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty reservation list")
	}
	reservations := make([]app.Reservation, len(result.Reservations))
	for i, reservation := range result.Reservations {
		reservations[i] = app.Reservation{RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy, Path: reservation.Path, Token: reservation.Token, Owner: reservation.Owner, CreatedAt: reservation.CreatedAt, CanRelease: reservation.CanRelease, LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport}
	}
	return reservations, nil
}

func (adapter reservationAdapter) ReleaseReservation(ctx context.Context, request app.ReservationReleaseRequest) error {
	return adapter.client.RepoReservationRelease(ctx, contract.RepoReservationReleasePayload{ServerID: request.ServerID, RepoID: request.RepoID, Path: request.Path, ExpectedToken: request.ExpectedToken, ConfirmRisk: request.ConfirmRisk})
}

func (adapter repositoryDetachAdapter) DetachRepository(ctx context.Context, serverID, repoID string, deleteRepository bool) error {
	operationCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	var (
		result *contract.RepoLifecycleResult
		err    error
	)
	if deleteRepository {
		result, err = adapter.client.RepoDelete(operationCtx, serverID, repoID)
	} else {
		result, err = adapter.client.RepoDetach(operationCtx, serverID, repoID)
	}
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty repository detach result")
	}
	expectedState := "detached"
	if deleteRepository {
		expectedState = "deleted"
	}
	if result.State != expectedState {
		if result.LastError != "" {
			return errors.New(result.LastError)
		}
		return errors.New("daemon did not complete repository detach")
	}
	return nil
}

type systemLifecycleClient interface {
	SystemRestart(context.Context) (*contract.SystemLifecycleResult, error)
	SystemShutdown(context.Context) (*contract.SystemLifecycleResult, error)
}

type stackLifecycleAdapter struct{ client systemLifecycleClient }

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

type mobilePairingClient interface {
	MobilePairingBegin(context.Context, string) (*contract.MobilePairingBeginResult, error)
}

// mobilePairingAdapter fetches a pairing token from the daemon over IPC,
// then hands it to the separate filees-pair-gui helper process over its
// stdin - a single JSON line, written once, stdin closed immediately after
// (os/exec already gives a private, unnamed pipe invisible to argv/env/ps;
// see pkg/deploy/tunnel_linux.go for the analogous discipline used where a
// named FIFO is unavoidable). The helper owns the rest of the flow (PIN
// gate, QR rendering) entirely on its own - this adapter does not wait for
// it to exit.
type mobilePairingAdapter struct {
	client     mobilePairingClient
	helperPath func() (string, error)
}

// defaultPairingHelperPath resolves the helper binary next to the running
// filees-gui executable, so it works identically whether installed via
// install-user.sh or run from a dist/ staging tree during development.
func defaultPairingHelperPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "filees-pair-gui"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

func (adapter mobilePairingAdapter) Launch(ctx context.Context, serverID string) error {
	result, err := adapter.client.MobilePairingBegin(ctx, serverID)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty mobile pairing result")
	}
	helperPathFunc := adapter.helperPath
	if helperPathFunc == nil {
		helperPathFunc = defaultPairingHelperPath
	}
	helperPath, err := helperPathFunc()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Address       string `json:"address"`
		HostPublicKey string `json:"host_public_key"`
		Token         string `json:"token"`
		ExpiresAt     string `json:"expires_at"`
	}{Address: result.Address, HostPublicKey: result.HostPublicKey, Token: result.Token, ExpiresAt: result.ExpiresAt})
	if err != nil {
		return err
	}
	defer clear(payload)
	cmd := exec.Command(helperPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write(payload); err != nil {
		stdin.Close()
		_ = cmd.Wait()
		return err
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Wait()
		return err
	}
	// Reap the helper process in the background - Launch does not wait for
	// the helper's own UI lifetime (PIN entry, QR display, timeout), only
	// for the handoff to succeed.
	go func() { _ = cmd.Wait() }()
	return nil
}

type updateAdapter struct{ client updateClient }

func (adapter updateAdapter) UpdatePlan(ctx context.Context) (*actions.UpdatePlan, error) {
	result, err := adapter.client.UpdatePlan(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update plan")
	}
	plan := &actions.UpdatePlan{
		CurrentVersion: result.CurrentVersion, AvailableVersion: result.AvailableVersion,
		ReleaseID: result.ReleaseID, RestartRequired: result.RestartRequired,
		Changes: make([]actions.UpdateChange, len(result.Changes)),
	}
	for index, change := range result.Changes {
		plan.Changes[index] = actions.UpdateChange{Action: change.Action, Path: change.Path, Detail: change.Detail}
	}
	return plan, nil
}

func (adapter updateAdapter) UpdateApply(ctx context.Context) (*actions.UpdateResult, error) {
	result, err := adapter.client.UpdateApply(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update result")
	}
	return &actions.UpdateResult{InstalledVersion: result.InstalledVersion, RestartRequired: result.RestartRequired}, nil
}

func (s *viewStore) load() app.ViewModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vm
}

func (s *viewStore) store(vm app.ViewModel) {
	s.mu.Lock()
	s.vm = vm
	s.mu.Unlock()
}

// run owns the GUI process lifecycle. The native tray remains on the calling
// goroutine; app and intent loops are started only after the tray is ready.
func run(parent context.Context, deps dependencies) error {
	if deps.tray == nil || deps.platform == nil || deps.client == nil {
		return errors.New("incomplete GUI dependencies")
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	intents := make(chan tray.Intent, 64)
	views := &viewStore{vm: app.ViewModel{Icon: app.IconDisconnected}}
	var notificationPolicy notifications.Policy
	notificationQueue := make(chan platform.Notification, 64)
	renderer := tray.NewRenderer(deps.tray, deps.icons, intents, func(intent tray.Intent) {
		log.Printf("filees-gui: dropped tray intent kind=%s repo_id=%s", intent.Kind, intent.RepoID)
	})
	guiApp := app.New(app.Config{
		Client: deps.client,
		OnChange: func(vm app.ViewModel) {
			views.store(vm)
			renderer.Render(tray.BuildMenu(vm))
			for _, notification := range notificationPolicy.Observe(vm) {
				select {
				case notificationQueue <- notification:
				case <-ctx.Done():
					return
				}
			}
		},
	})
	restartRequested := make(chan struct{}, 1)
	var updater actions.Updater
	if candidate, ok := deps.client.(updateClient); ok {
		updater = updateAdapter{client: candidate}
	}
	var repositoryCreator actions.RepositoryCreator
	if candidate, ok := deps.client.(repositoryCreateClient); ok {
		repositoryCreator = repositoryCreateAdapter{client: candidate}
	}
	var repositoryDetacher actions.RepositoryDetacher
	if candidate, ok := deps.client.(repositoryDetachClient); ok {
		repositoryDetacher = repositoryDetachAdapter{client: candidate}
	}
	var reservations actions.ReservationManager
	if candidate, ok := deps.client.(reservationClient); ok {
		reservations = reservationAdapter{client: candidate}
	}
	var stack actions.StackLifecycle
	if candidate, ok := deps.client.(systemLifecycleClient); ok {
		stack = stackLifecycleAdapter{client: candidate}
	}
	var mobilePairer actions.MobilePairingLauncher
	if candidate, ok := deps.client.(mobilePairingClient); ok {
		mobilePairer = mobilePairingAdapter{client: candidate}
	}
	controller := actions.New(actions.Config{
		Intents:            intents,
		ViewModel:          views.load,
		Opener:             deps.platform,
		Picker:             deps.platform,
		FolderPicker:       deps.platform,
		Prompter:           deps.platform,
		RepositoryCreator:  repositoryCreator,
		RepositoryDetacher: repositoryDetacher,
		MobilePairer:       mobilePairer,
		PinStore:           deps.pinStore,
		Activator:          deps.activator,
		Updater:            updater,
		Stack:              stack,
		Notifier:           deps.platform,
		Locker:             deps.client,
		Reservations:       reservations,
		ReservationBrowser: deps.platform,
		Reconnect:          guiApp.Reconnect,
		PrepareRestart:     notificationPolicy.SuppressConnectionTransitions,
		AbortRestart:       notificationPolicy.RestoreConnectionTransitions,
		Restart: func() {
			select {
			case restartRequested <- struct{}{}:
			default:
			}
			cancel()
		},
		Shutdown: cancel,
	})

	var wg sync.WaitGroup
	var startOnce sync.Once
	onReady := func() {
		startOnce.Do(func() {
			renderer.Render(tray.BuildMenu(views.load()))
			wg.Add(3)
			go func() {
				defer wg.Done()
				guiApp.Run(ctx)
			}()
			go func() {
				defer wg.Done()
				controller.Run(ctx)
			}()
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case notification := <-notificationQueue:
						_ = deps.platform.Notify(ctx, notification)
					}
				}
			}()
		})
	}
	onExit := cancel

	trayStopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			deps.tray.Quit()
		case <-trayStopped:
		}
	}()
	deps.tray.Run(onReady, onExit)
	close(trayStopped)
	cancel()
	renderer.Close()
	wg.Wait()
	select {
	case <-restartRequested:
		return errGUIRestartRequested
	default:
	}
	return nil
}
