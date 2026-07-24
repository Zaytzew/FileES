package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"filees/internal/gui/identity"
	"filees/internal/gui/platform"
	"filees/internal/gui/singleinstance"
	"filees/internal/gui/tray"
	"filees/pkg/clientprofile"
	"filees/pkg/deploy"
	"filees/pkg/ipcclient"
	"filees/pkg/localpin"
)

var version = "dev"

func main() {
	flags := flag.NewFlagSet("filees-gui", flag.ContinueOnError)
	socket := flags.String("socket", ipcclient.DefaultSocketPath(), "ścieżka do gniazda IPC daemona")
	autostart := flags.String("autostart", "", "zarządzaj autostartem: status, enable albo disable")
	showVersion := flags.Bool("version", false, "pokaż wersję i zakończ")
	activationRoot := flags.String("activation-state", clientprofile.DefaultRoot(), "katalog stanu aktywacji klienta")
	serverID := flags.String("server-id", "", "identyfikator profilu serwera")
	serverAddress := flags.String("server", "", "adres SSH serwera FileES host:port")
	knownHosts := flags.String("known-hosts", "", "plik z pinowanym kluczem hosta")
	remotePort := flags.Int("activation-remote-port", 42000, "przydzielony port reverse tunnel")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, version)
		return
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

	instance, err := singleinstance.Acquire(identity.ID)
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: single instance: %v\n", err)
		os.Exit(1)
	}

	pinStore, pinErr := localpin.Open(localpin.DefaultRoot())
	if pinErr != nil {
		// Best-effort: the local-PIN feature (optional GUI-startup lock,
		// mandatory mobile-pairing gate) is simply unavailable, never a
		// reason to refuse starting the GUI entirely.
		fmt.Fprintf(os.Stderr, "filees-gui: local PIN store unavailable: %v\n", pinErr)
		pinStore = nil
	}
	if err := requireLocalPinAtLaunch(context.Background(), platformBackend, pinStore); err != nil {
		instance.Close()
		fmt.Fprintf(os.Stderr, "filees-gui: %v\n", err)
		os.Exit(1)
	}

	trayBackend, err := tray.NewSystrayBackend()
	if err != nil {
		instance.Close()
		fmt.Fprintf(os.Stderr, "filees-gui: tray: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	daemonClient := ipcclient.New(*socket, "filees-gui")
	runErr := run(ctx, dependencies{
		tray:     trayBackend,
		platform: platformBackend,
		client:   daemonClient,
		icons:    tray.PlatformIcons(),
		activator: clientActivator{client: daemonClient, root: *activationRoot, remotePort: *remotePort, profile: deploy.ServerProfile{
			ID: *serverID, Address: *serverAddress, KnownHostsPath: *knownHosts,
		}},
		pinStore: pinStore,
	})
	closeErr := instance.Close()
	if errors.Is(runErr, errGUIRestartRequested) {
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "filees-gui: release instance lock before restart: %v\n", closeErr)
			os.Exit(1)
		}
		if err := launchReplacement(os.Args); err != nil {
			fmt.Fprintf(os.Stderr, "filees-gui: restart: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: release instance lock: %v\n", closeErr)
		os.Exit(1)
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: %v\n", runErr)
		os.Exit(1)
	}
}

func launchReplacement(argv []string) error {
	if len(argv) == 0 {
		return errors.New("missing GUI process arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	command := exec.Command(executable, argv[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func newAutostartSpec(executable, socket string) platform.AutostartSpec {
	return platform.AutostartSpec{
		ID:         identity.ID,
		Name:       identity.Name,
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
			if !state.Current {
				label = "enabled-stale"
			}
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
