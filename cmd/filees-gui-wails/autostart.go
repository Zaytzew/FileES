package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/gui/identity"
	"filees/internal/gui/platform"
)

// The installer is the only caller: `filees-gui --autostart enable` is the last
// step of install-user.sh and of the AppImage's first launch, and the Windows
// package asks for the same thing. The predecessor of this binary answered it;
// when the interface moved to Wails the flag was left behind, so every Linux
// installation ended at an unknown flag - the AppImage never reached the
// interface on first launch and nothing ever wrote the autostart entry. The
// platform backends kept their implementation the whole time, so this is the
// one missing edge, not a second way to switch autostart on.

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
				// Enabled, but pointing at something other than this
				// installation - worth saying out loud rather than reporting
				// plain success.
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

// runAutostartMode serves the flag and reports whether the process is done.
func runAutostartMode(mode, socket string) bool {
	if strings.TrimSpace(mode) == "" {
		return false
	}
	executable, err := os.Executable()
	if err == nil {
		err = manageAutostart(context.Background(), newActionPlatform(), mode, newAutostartSpec(executable, socket), os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-gui: autostart: %v\n", err)
		os.Exit(1)
	}
	return true
}
