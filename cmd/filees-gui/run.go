package main

import (
	"context"
	"errors"
	"log"
	"sync"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/notifications"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

var errGUIRestartRequested = errors.New("GUI restart requested")

type dependencies struct {
	tray      tray.Backend
	platform  platform.Backend
	client    app.DaemonClient
	icons     tray.IconSet
	activator actions.Activator
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
	controller := actions.New(actions.Config{
		Intents:           intents,
		ViewModel:         views.load,
		Opener:            deps.platform,
		Picker:            deps.platform,
		FolderPicker:      deps.platform,
		Prompter:          deps.platform,
		RepositoryCreator: repositoryCreator,
		Activator:         deps.activator,
		Updater:           updater,
		Notifier:          deps.platform,
		Locker:            deps.client,
		Reconnect:         guiApp.Reconnect,
		Quit: func() {
			cancel()
		},
		Restart: func() {
			select {
			case restartRequested <- struct{}{}:
			default:
			}
			cancel()
		},
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
