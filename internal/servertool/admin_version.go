package servertool

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	installconfig "filees/internal/serverinstall/config"
	installstate "filees/internal/serverinstall/state"
)

// IsAdminVersionRequest keeps every version spelling outside the administrative
// identity transition, including requests whose options will be rejected.
func IsAdminVersionRequest(args []string) bool {
	return len(args) > 0 && (args[0] == "version" || args[0] == "--version" || args[0] == "-version")
}

func runAdminVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("install-config", installconfig.DefaultConfigPath, "installer configuration (read-only channel and release metadata)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return ExitUsage
	}
	fmt.Fprintf(stdout, "filees-admin %s\nplatform: %s/%s\n", adminVersion, runtime.GOOS, runtime.GOARCH)
	channel, release := "unknown", "unknown"
	// No search in the current directory: an unrelated local config must not
	// impersonate the machine's installation. Never load server.json/secrets.
	cfg, err := installconfig.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, "filees-admin version: installer configuration unavailable; update channel and recorded release are unknown")
	} else {
		if strings.TrimSpace(cfg.Channel) != "" {
			channel = cfg.Channel
		}
		st, err := installstate.Load(cfg.StateDir)
		if err != nil {
			fmt.Fprintln(stderr, "filees-admin version: installer state unavailable; recorded release is unknown")
		} else if st.InstalledRelease != "" {
			release = st.InstalledRelease
		}
	}
	// These are installation metadata, not proof that every resident service
	// has restarted or that this binary matches the installer's recorded release.
	fmt.Fprintf(stdout, "update_channel: %s\nrecorded_release: %s\n", channel, release)
	return ExitOK
}
