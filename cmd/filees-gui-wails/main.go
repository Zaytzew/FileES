package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"filees/pkg/ipcclient"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

var version = "dev"

// The complete frontend is embedded in the client executable. There is no
// Node/Vite build step: CSS and browser modules keep the shipping client
// independent of a second build toolchain.
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
	promptBridge := newPromptBridge(prompts)
	restartRequested := make(chan struct{}, 1)

	host := application.New(application.Options{
		Name:        "FileES Wails",
		Description: "Klient FileES z interfejsem Wails",
		Services: []application.Service{
			application.NewService(gui),
			application.NewService(settings),
			application.NewService(repository),
			application.NewService(promptBridge),
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
	darkTheme := systemPrefersDark(host.Env.IsDarkMode())
	themeJS := systemThemeScript(darkTheme)
	themeBackground := systemThemeBackground(darkTheme)
	gui.attachEmitter(host.Event)
	settings.attachEmitter(host.Event)
	repository.attachEmitter(host.Event)
	prompts.attachEmitter(host.Event)

	mainWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "filees-main",
		Title:            "FileES",
		URL:              "/",
		Width:            1180,
		Height:           780,
		MinWidth:         820,
		MinHeight:        620,
		Frameless:        true,
		JS:               themeJS,
		BackgroundColour: themeBackground,
		DevToolsEnabled:  *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	settingsWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "filees-settings",
		Title:            "Ustawienia FileES",
		URL:              "/settings.html",
		Width:            940,
		Height:           720,
		MinWidth:         760,
		MinHeight:        560,
		Frameless:        true,
		Hidden:           true,
		JS:               themeJS,
		BackgroundColour: themeBackground,
		DevToolsEnabled:  *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	repositoryWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "filees-repository",
		Title:            "Działania folderu FileES",
		URL:              "/repository.html",
		Width:            1040,
		Height:           720,
		MinWidth:         780,
		MinHeight:        560,
		Frameless:        true,
		Hidden:           true,
		JS:               themeJS,
		BackgroundColour: themeBackground,
		DevToolsEnabled:  *devtools,
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
	})
	promptWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "filees-prompt", Title: "FileES", URL: "/prompt.html",
		Width: 640, Height: 430, MinWidth: 520, MinHeight: 340,
		Frameless: true, Hidden: true, AlwaysOnTop: true, DisableResize: true,
		JS: themeJS, BackgroundColour: themeBackground,
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

	nativePicker := newWailsFolderPicker(host.Dialog)
	shouts := shoutAdapter{client: daemon}
	realmGrants := realmGrantAdapter{client: daemon}
	actionPlatform := newActionPlatform()
	actionController := configureActions(
		gui, daemon, reservationAdapter{client: daemon}, stackLifecycleAdapter{client: daemon}, updateAdapter{client: daemon}, shouts, shouts, realmGrants, repositoryRealmGrantBrowserAdapter{service: repository, fallback: actionPlatform}, realmBrandingAdapter{client: daemon},
		settingsBrowserRouter{server: settingsBrowserAdapter{service: settings}, repository: repositorySettingsBrowserAdapter{service: repository}},
		sessionTimeoutAdapter{client: daemon}, repositoryPublicShareBrowserAdapter{service: repository}, publicShareAdapter{client: daemon}, repositoryUploadChannelBrowserAdapter{service: repository}, uploadChannelAdapter{client: daemon}, repositoryCreateAdapter{client: daemon}, repositoryAttachAdapter{client: daemon}, repositoryLocateAdapter{client: daemon}, repositoryDetachAdapter{client: daemon}, repositoryDumpLoadAdapter{client: daemon}, serverDetachAdapter{client: daemon}, realmRemovalAdapter{client: daemon}, recoveryDownloadAdapter{client: daemon}, consentPromptAdapter{prompter: prompts}, actionPlatform, nativePicker, nativePicker, prompts,
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
	host.Event.OnApplicationEvent(events.Common.ThemeChanged, func(event *application.ApplicationEvent) {
		dark := systemPrefersDark(event.Context().IsDarkMode())
		applySystemTheme(dark, mainWindow, settingsWindow, repositoryWindow, promptWindow)
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

func systemPrefersDark(wailsDark bool) bool {
	if wailsDark || runtime.GOOS != "linux" {
		return wailsDark
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "color-scheme").Output()
	return err == nil && linuxColorSchemeIsDark(string(output))
}

func linuxColorSchemeIsDark(value string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(value), "'\""), "prefer-dark")
}

func systemThemeScript(dark bool) string {
	theme := "light"
	if dark {
		theme = "dark"
	}
	return fmt.Sprintf(`document.documentElement.dataset.systemTheme=%q;`, theme)
}

func systemThemeBackground(dark bool) application.RGBA {
	if dark {
		return application.NewRGB(0x07, 0x10, 0x1d)
	}
	return application.NewRGB(0xed, 0xf2, 0xf6)
}

func applySystemTheme(dark bool, windows ...*application.WebviewWindow) {
	script := systemThemeScript(dark)
	background := systemThemeBackground(dark)
	for _, window := range windows {
		window.SetBackgroundColour(background)
		window.ExecJS(script)
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
