package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	"filees/pkg/ipcclient"
)

func main() {
	flags := flag.NewFlagSet("filees-gui", flag.ContinueOnError)
	socket := flags.String("socket", ipcclient.DefaultSocketPath(), "ścieżka do gniazda IPC daemona")
	autostart := flags.String("autostart", "", "zarządzaj autostartem: status, enable albo disable")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	platformBackend, err := newPlatformBackend()
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: platform: %v\n", err)
		os.Exit(1)
	}
	if *autostart != "" {
		executable, execErr := os.Executable()
		if execErr == nil {
			executable, execErr = filepath.Abs(executable)
		}
		if execErr == nil {
			execErr = manageAutostart(context.Background(), platformBackend, *autostart,
				newAutostartSpec(executable, *socket), os.Stdout)
		}
		if execErr != nil {
			fmt.Fprintf(os.Stderr, "filees-gui: autostart: %v\n", execErr)
			os.Exit(1)
		}
		return
	}

	trayBackend, err := tray.NewSystrayBackend()
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: tray: %v\n", err)
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

func newAutostartSpec(executable, socket string) platform.AutostartSpec {
	return platform.AutostartSpec{
		ID:         "filees-gui",
		Name:       "FileES",
		Executable: filepath.Clean(executable),
		Args:       []string{"--socket", socket},
	}
}

func manageAutostart(ctx context.Context, backend platform.Autostart, mode string, spec platform.AutostartSpec, out io.Writer) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "status":
		state, err := backend.AutostartStatus(ctx, spec)
		if err != nil {
			return err
		}
		label := "disabled"
		if state.Enabled {
			label = "enabled"
		}
		_, err = fmt.Fprintf(out, "autostart: %s (%s)\n", label, state.Source)
		return err
	case "enable":
		return backend.SetAutostart(ctx, spec, true)
	case "disable":
		return backend.SetAutostart(ctx, spec, false)
	default:
		return fmt.Errorf("unknown mode %q (use status, enable or disable)", mode)
	}
}
