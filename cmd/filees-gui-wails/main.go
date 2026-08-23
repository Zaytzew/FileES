package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"os"

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
	actionController := configureActions(gui, daemon, newActionPlatform())

	host := application.New(application.Options{
		Name:        "FileES Wails",
		Description: "Eksperymentalny renderer IPC FileES",
		Services: []application.Service{
			application.NewService(gui),
		},
		Assets: application.AssetOptions{
			Handler:        application.BundledAssetFileServer(frontend),
			DisableLogging: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	gui.attachEmitter(host.Event)
	host.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go gui.run(host.Context())
		if actionController != nil {
			go actionController.Run(host.Context())
		}
	})

	host.Window.NewWithOptions(application.WebviewWindowOptions{
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

	if err := host.Run(); err != nil {
		log.Fatal(err)
	}
}
