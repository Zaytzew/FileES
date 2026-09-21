package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filees/internal/gui/platform"
	"filees/pkg/config"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Installer-only mode: no IPC, projection, tray, activation or live daemon.
// The installer passes a provisional config, promoting it only on success.
func runInitialChannelWindow(path string) error {
	view, err := config.LoadClientView(path)
	if err != nil {
		return err
	}
	if view.UpdateConfigured { // Includes an explicit opt-out.
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	name := "filees"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	daemon := filepath.Join(filepath.Dir(executable), name)
	prompts := newPromptService()
	host := application.New(application.Options{
		Name: "FileES setup", Icon: appIcon,
		Services: []application.Service{application.NewService(newPromptBridge(prompts))},
		Assets:   application.AssetOptions{Handler: application.BundledAssetFileServer(frontend), DisableLogging: true},
	})
	dark := systemPrefersDark(host.Env.IsDarkMode())
	window := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "filees-channel", Title: "FileES", URL: "/prompt.html",
		Width: 680, Height: 560, MinWidth: 560, MinHeight: 460,
		Frameless:        true,
		JS:               systemThemeScript(dark) + systemLanguageScript(systemLanguages()),
		BackgroundColour: systemThemeBackground(dark),
		Windows:          application.WindowsWindow{NonClientRegionSupport: true},
	})
	prompts.attachEmitter(host.Event)
	prompts.attachPresentation(func() { window.Show(); window.Center(); window.Focus() }, nil)
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		prompts.Cancel()
	})
	result := make(chan error, 1)
	var started sync.Once
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(_ *application.WindowEvent) {
		started.Do(func() {
			go func() {
				result <- chooseInitialChannel(context.Background(), prompts, func(channel string) error {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, daemon, "update-channel", channel, "--config", path)
					prepareChannelCommand(cmd)
					output, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
					}
					return nil
				})
				host.Quit()
			}()
		})
	})
	if err := host.Run(); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	default:
		return errors.New("channel selection interrupted")
	}
}

type initialChannelPrompter interface {
	SelectOne(context.Context, PromptSelectRequest) (PromptSelectResult, error)
	ShowInfo(context.Context, platform.InfoRequest) error
}

func chooseInitialChannel(ctx context.Context, prompts initialChannelPrompter, save func(string) error) error {
	choice, err := prompts.SelectOne(ctx, PromptSelectRequest{
		PresentationKey: "select.updateChannel", Title: "FileES", Default: "beta",
		Options: []PromptOption{{Value: "beta", Label: "Beta"}, {Value: "alpha", Label: "Alpha"}},
	})
	if err != nil {
		return err
	}
	if choice.Cancelled {
		return errors.New("channel selection cancelled; application was not started")
	}
	if choice.Value != "alpha" && choice.Value != "beta" {
		return errors.New("unsupported initial update channel")
	}
	if err := save(choice.Value); err != nil {
		_ = prompts.ShowInfo(ctx, platform.InfoRequest{
			PresentationKey:  "info.updateChannelFailed",
			PresentationArgs: map[string]string{"reason": err.Error()},
		})
		return err
	}
	return nil
}
