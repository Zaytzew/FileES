package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	rootversion "filees"
	"filees/internal/gui/clientactivation"
	"filees/internal/gui/projectionmirror"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/localpin"
	"filees/pkg/opjournal"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// version may be overridden by the trusted build pipeline through -ldflags
// -X. Native development builds fall back to the root VERSION embedded by
// the filees package instead of carrying a second hard-coded version.
var version string

func clientVersion() string {
	if candidate := strings.TrimSpace(version); candidate != "" {
		return candidate
	}
	return rootversion.Version()
}

// The complete frontend is embedded in the client executable. There is no
// Node/Vite build step: CSS and browser modules keep the shipping client
// independent of a second build toolchain.
//
//go:embed frontend
var frontend embed.FS

// appIcon is what Windows shows on the taskbar button and in Alt-Tab.
//
// It has to be handed to Wails explicitly because nothing else supplies it.
// The window code looks for icon resource 3 inside the executable first, and a
// Go binary carries no icon resource unless one is generated and linked in;
// only then does it fall back to application.Options.Icon. With neither, the
// taskbar entry appeared with no icon at all - present, blank, and no error
// anywhere to say why.
//
// Setting the option is the whole fix, and it avoids a generated .syso and the
// build-time tool that produces one. PNG rather than the .ico beside it:
// Windows takes icon *resource* bytes here, and an .ico file is a container
// with a directory in front of the images.
//
//go:embed assets/app-icon.png
var appIcon []byte

func main() {
	flags := flag.NewFlagSet("filees-gui-wails", flag.ContinueOnError)
	socket := flags.String("socket", ipcclient.DefaultSocketPath(), "ścieżka do gniazda IPC demona")
	activationRoot := flags.String("activation-state", clientprofile.DefaultRoot(), "katalog stanu aktywacji klienta")
	showVersion := flags.Bool("version", false, "pokaż wersję i zakończ")
	devtools := flags.Bool("devtools", false, "włącz narzędzia deweloperskie WebView")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, clientVersion())
		return
	}

	daemon := ipcclient.New(*socket, "filees-gui-wails")
	mirror, mirrorErr := projectionmirror.Open(filepath.Join(filepath.Dir(*activationRoot), "projection-mirror"))
	if mirrorErr != nil {
		log.Printf("filees-gui-wails: projection mirror unavailable: %v", mirrorErr)
		mirror = nil
	}
	profiles, profileErr := clientprofile.List(*activationRoot)
	if profileErr != nil {
		log.Printf("filees-gui-wails: offline activation projection unavailable: %v", profileErr)
	}
	offlineActivations := make([]contract.ActivationStatus, 0, len(profiles))
	activeServerIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		displayName := profile.DisplayName
		if strings.TrimSpace(displayName) == "" {
			displayName = profile.ServerID
		}
		offlineActivations = append(offlineActivations, contract.ActivationStatus{
			ServerID: profile.ServerID, DisplayName: displayName, Address: profile.Address,
			ClientID: profile.ClientID, SSHPort: profile.SSHPort,
		})
		activeServerIDs = append(activeServerIDs, profile.ServerID)
	}
	if mirror != nil {
		if _, err := mirror.Prune(activeServerIDs); err != nil {
			log.Printf("filees-gui-wails: projection mirror prune: %v", err)
		}
	}
	pinStore, pinErr := localpin.Open(localpin.DefaultRoot())
	if pinErr != nil {
		log.Printf("filees-gui-wails: local PIN store unavailable: %v", pinErr)
		pinStore = nil
	}
	gui := newGUIServiceWithProjection(daemon, mirror, offlineActivations)
	settings := newSettingsService()
	repository := newRepositoryService()
	prompts := newPromptService()
	promptBridge := newPromptBridge(prompts)
	pairing := newPairingService()
	restartRequested := make(chan struct{}, 1)

	host := application.New(application.Options{
		Name:        "FileES Wails",
		Description: "Klient FileES z interfejsem Wails",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(gui),
			application.NewService(settings),
			application.NewService(repository),
			application.NewService(promptBridge),
			application.NewService(newPairingBridge(pairing)),
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
	pairing.attachEmitter(host.Event)

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
	pairingWindow := host.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "filees-pairing", Title: "Sparuj urządzenie mobilne — FileES", URL: "/pairing.html",
		Width: 520, Height: 620, MinWidth: 480, MinHeight: 560,
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
	pairing.attachPresentation(func() {
		pairingWindow.Show()
		pairingWindow.Center()
		pairingWindow.Focus()
	}, func() { pairingWindow.Hide() })
	pairingWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		pairing.Cancel()
	})

	nativePicker := newWailsFolderPicker(host.Dialog)
	shouts := shoutAdapter{client: daemon}
	realmGrants := realmGrantAdapter{client: daemon}
	actionPlatform := newActionPlatform()
	actionController := configureActions(
		gui, daemon, reservationAdapter{client: daemon}, lockReleaseAdapter{client: daemon}, stackLifecycleAdapter{client: daemon}, updateAdapter{client: daemon}, clientactivation.New(daemon, *activationRoot).WithFailureReporter(func(step string, err error) {
			// The interface records only what the daemon cannot: that the call
			// to it did not come back. Wired here because internal/gui must not
			// reach the engine, and the operational log is an engine concern.
			opjournal.RecordFailure("activation", "Interfejs nie doczekał się odpowiedzi demona podczas aktywacji ("+step+")", err)
		}), pinStore, mobilePairingAdapter{client: daemon, pinStore: pinStore, prompter: prompts, presenter: pairing, servers: func() []PromptOption { return pairingServerOptions(gui.Snapshot()) }}, shouts, shouts, realmAliasAdapter{client: daemon}, realmGrants, repositoryRealmGrantBrowserAdapter{service: repository, fallback: actionPlatform}, realmBrandingAdapter{client: daemon},
		settingsBrowserRouter{server: settingsBrowserAdapter{service: settings}, repository: repositorySettingsBrowserAdapter{service: repository}},
		sessionTimeoutAdapter{client: daemon}, repositoryPublicShareBrowserAdapter{service: repository}, publicShareAdapter{client: daemon}, repositoryUploadChannelBrowserAdapter{service: repository}, uploadChannelAdapter{client: daemon}, repositoryQuarantineBrowserAdapter{service: repository}, quarantineAdapter{client: daemon}, repositoryCreateAdapter{client: daemon}, repositoryAttachAdapter{client: daemon}, repositoryLocateAdapter{client: daemon}, repositoryDetachAdapter{client: daemon}, repositoryLifecycleRepairAdapter{client: daemon}, repositoryDumpLoadAdapter{client: daemon}, serverDetachAdapter{client: daemon}, realmRemovalAdapter{client: daemon}, recoveryDownloadAdapter{client: daemon}, consentPromptAdapter{prompter: prompts}, actionPlatform, nativePicker, nativePicker, prompts,
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
		applySystemTheme(dark, mainWindow, settingsWindow, repositoryWindow, promptWindow, pairingWindow)
	})
	configureWailsTray(host, mainWindow, gui, actionPlatform)

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
