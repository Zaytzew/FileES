package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"

	"filees/pkg/ipcclient"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

var version = "dev"

// The complete frontend is embedded in the experimental executable.  There is
// intentionally no Node/Vite build step in this first fork: CSS and browser
// modules are enough to test the renderer without introducing another toolchain.
//
//go:embed frontend
var frontend embed.FS

func main() {
	flags := flag.NewFlagSet("filees-gui-wails", flag.ContinueOnError)
	socket := flags.String("socket", ipcclient.DefaultSocketPath(), "ścieżka do gniazda IPC demona")
	showVersion := flags.Bool("version", false, "pokaż wersję i zakończ")
	devtools := flags.Bool("devtools", false, "włącz narzędzia deweloperskie WebView")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return
	}

	daemon := ipcclient.New(*socket, "filees-gui-wails")
	gui := newGUIService(daemon)
	settings := newSettingsService()
	repository := newRepositoryService()
	prompts := newPromptService()
	restartRequested := make(chan struct{}, 1)

	host := application.New(application.Options{
		Name:        "FileES Wails",
		Description: "Eksperymentalny renderer IPC FileES",
		Services: []application.Service{
			application.NewService(gui),
			application.NewService(settings),
			application.NewService(repository),
			application.NewService(prompts),
		},
		Assets: application.AssetOptions{
			Handler:        application.BundledAssetFileServer(frontend),
			DisableLogging: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: true},
	})
	gui.attachEmitter(host.Event)
	settings.attachEmitter(host.Event)
	repository.attachEmitter(host.Event)
	prompts.attachEmitter(host.Event)

	mainWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "filees-main",
		Title:           "FileES — Wails renderer",
		URL:             "/",
		Width:           1180,
		Height:          780,
		MinWidth:        820,
		MinHeight:       620,
		Frameless:       true,
		DevToolsEnabled: *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	settingsWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "filees-settings",
		Title:           "Ustawienia FileES",
		URL:             "/settings.html",
		Width:           940,
		Height:          720,
		MinWidth:        760,
		MinHeight:       560,
		Frameless:       true,
		Hidden:          true,
		DevToolsEnabled: *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	repositoryWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "filees-repository",
		Title:           "Działania folderu FileES",
		URL:             "/repository.html",
		Width:           1040,
		Height:          720,
		MinWidth:        780,
		MinHeight:       560,
		Frameless:       true,
		Hidden:          true,
		DevToolsEnabled: *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	promptWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "filees-prompt", Title: "FileES", URL: "/prompt.html",
		Width: 640, Height: 430, MinWidth: 520, MinHeight: 340,
		Frameless: true, Hidden: true, AlwaysOnTop: true, DisableResize: true,
		DevToolsEnabled: *devtools,
		Windows:         application.WindowsWindow{NonClientRegionSupport: true},
	})
	settings.attachPresentation(func() {
		settingsWindow.Show()
		settingsWindow.Center()
		settingsWindow.Focus()
	}, func() { settingsWindow.Hide() })
	settingsWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		settings.Cancel()
	})
	repository.attachPresentation(func() {
		repositoryWindow.Show()
		repositoryWindow.Center()
		repositoryWindow.Focus()
	}, func() { repositoryWindow.Hide() })
	repositoryWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		repository.Cancel()
	})
	prompts.attachPresentation(func() {
		promptWindow.Show()
		promptWindow.Center()
		promptWindow.Focus()
	}, func() { promptWindow.Hide() })
	promptWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		prompts.Cancel()
	})

	actionController := configureActions(
		gui, daemon, reservationAdapter{client: daemon}, stackLifecycleAdapter{client: daemon},
		settingsBrowserRouter{server: settingsBrowserAdapter{service: settings}, repository: repositorySettingsBrowserAdapter{service: repository}},
		sessionTimeoutAdapter{client: daemon}, repositoryPublicShareBrowserAdapter{service: repository}, publicShareAdapter{client: daemon}, repositoryAttachAdapter{client: daemon}, repositoryDetachAdapter{client: daemon}, recoveryDownloadAdapter{client: daemon}, newActionPlatform(), prompts,
		func() {
			select {
			case restartRequested <- struct{}{}:
			default:
			}
			host.Quit()
		},
		host.Quit,
	)

	host.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go gui.run(host.Context())
		if actionController != nil {
			go actionController.Run(host.Context())
		}
	})
	configureWailsTray(host, mainWindow, gui)

	if err := host.Run(); err != nil {
		log.Fatal(err)
	}
	select {
	case <-restartRequested:
		if err := restartCurrentProcess(os.Args); err != nil {
			log.Printf("filees-gui-wails: restart: %v", err)
		}
	default:
	}
}

func restartCurrentProcess(argv []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string(nil)
	if len(argv) > 1 {
		args = append(args, argv[1:]...)
	}
	command := exec.Command(executable, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
