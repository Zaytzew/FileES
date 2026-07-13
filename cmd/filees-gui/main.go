package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"filees/internal/gui/tray"
	"filees/pkg/ipcclient"
)

func main() {
	flags := flag.NewFlagSet("filees-gui", flag.ContinueOnError)
	socket := flags.String("socket", ipcclient.DefaultSocketPath(), "ścieżka do gniazda IPC daemona")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	trayBackend, err := tray.NewSystrayBackend()
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: tray: %v\n", err)
		os.Exit(1)
	}
	platformBackend, err := newPlatformBackend()
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: platform: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, dependencies{
		tray:     trayBackend,
		platform: platformBackend,
		client:   ipcclient.New(*socket, "filees-gui"),
		icons:    tray.PlatformIcons(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: %v\n", err)
		os.Exit(1)
	}
}
