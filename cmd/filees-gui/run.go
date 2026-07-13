package main

import (
	"context"
	"errors"
	"sync"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
)

type dependencies struct {
	tray     tray.Backend
	platform platform.Backend
	client   app.DaemonClient
	icons    tray.IconSet
}

type viewStore struct {
	mu sync.RWMutex
	vm app.ViewModel
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
	renderer := tray.NewRenderer(deps.tray, deps.icons, intents)
	guiApp := app.New(app.Config{
		Client: deps.client,
		OnChange: func(vm app.ViewModel) {
			views.store(vm)
			renderer.Render(tray.BuildMenu(vm))
		},
	})
	controller := actions.New(actions.Config{
		Intents:   intents,
		ViewModel: views.load,
		Opener:    deps.platform,
		Picker:    deps.platform,
		Notifier:  deps.platform,
		Locker:    deps.client,
		Reconnect: guiApp.Reconnect,
		Quit: func() {
			cancel()
			deps.tray.Quit()
		},
	})

	var wg sync.WaitGroup
	var startOnce sync.Once
	onReady := func() {
		startOnce.Do(func() {
			renderer.Render(tray.BuildMenu(views.load()))
			wg.Add(2)
			go func() {
				defer wg.Done()
				guiApp.Run(ctx)
			}()
			go func() {
				defer wg.Done()
				controller.Run(ctx)
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
	return nil
}
