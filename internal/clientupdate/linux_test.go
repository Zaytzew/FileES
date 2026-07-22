//go:build !windows

package clientupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/releaseenvelope"
)

type commandCall struct {
	program, directory string
	environment        []string
}

type commandRunnerStub struct{ calls []commandCall }

func (runner *commandRunnerStub) Run(_ context.Context, program, directory string, environment []string) error {
	runner.calls = append(runner.calls, commandCall{program: program, directory: directory, environment: append([]string(nil), environment...)})
	return nil
}

func linuxBundle(t *testing.T) ([]byte, *releaseenvelope.Resolved) {
	t.Helper()
	entries := []tarEntry{
		{name: "install-user.sh", typeflag: 0, mode: 0o755, data: "#!/bin/sh\n"},
		{name: "SHA256SUMS", typeflag: 0, mode: 0o644, data: "checksums\n"},
		{name: "VERSION", typeflag: 0, mode: 0o644, data: "1.1\n"},
		{name: "bin/filees", typeflag: 0, mode: 0o755, data: "daemon-new"},
		{name: "bin/filees-gui", typeflag: 0, mode: 0o755, data: "gui-new"},
		{name: "share/icons/hicolor/scalable/apps/filees-gui.svg", typeflag: 0, mode: 0o644, data: "<svg/>"},
		{name: "share/applications/filees-gui.desktop", typeflag: 0, mode: 0o644, data: "desktop"},
		{name: "share/systemd/user/filees.service", typeflag: 0, mode: 0o644, data: "unit"},
		{name: "share/filees/config.example.json", typeflag: 0, mode: 0o644, data: "{}"},
	}
	bundle := makeBundle(t, entries...)
	return bundle, stagedRelease(bundle)
}

func TestLinuxInstallerPlansExistingLifecycleAndPreservesConfig(t *testing.T) {
	bundle, resolved := linuxBundle(t)
	home := t.TempDir()
	paths := LinuxPaths{Home: home, Prefix: filepath.Join(home, ".local"), DataHome: filepath.Join(home, ".local", "share"), ConfigHome: filepath.Join(home, ".config")}
	for path, data := range map[string]string{
		filepath.Join(paths.Prefix, "bin", "filees"):             "daemon-old",
		filepath.Join(paths.Prefix, "bin", "filees-gui"):         "gui-new",
		filepath.Join(paths.ConfigHome, "filees", "config.json"): `{"owned":"user"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	artifactPath := "releases/r1/desktop/linux-amd64/client.tar.gz"
	installer := LinuxInstaller{Stager: BundleStager{Fetcher: artifactFetcher{artifactPath: bundle}, Root: t.TempDir()}, Paths: paths}
	changes, restart, err := installer.Plan(context.Background(), resolved)
	if err != nil || !restart {
		t.Fatalf("Plan() = %+v, %v, restart=%v", changes, err, restart)
	}
	actions := make(map[string]string)
	for _, change := range changes {
		actions[change.Path] = change.Action
	}
	if actions[filepath.Join(paths.Prefix, "bin", "filees")] != "update" || actions[filepath.Join(paths.Prefix, "bin", "filees-gui")] != "unchanged" {
		t.Fatalf("binary actions = %+v", actions)
	}
	if actions[filepath.Join(paths.ConfigHome, "filees", "config.json")] != "unchanged" {
		t.Fatalf("config would not be preserved: %+v", actions)
	}
}

func TestLinuxInstallerApplyRunsBundledInstallerWithControlledLifecycle(t *testing.T) {
	bundle, resolved := linuxBundle(t)
	home := t.TempDir()
	paths := LinuxPaths{Home: home, Prefix: filepath.Join(home, ".local"), DataHome: filepath.Join(home, ".data"), ConfigHome: filepath.Join(home, ".config")}
	runner := &commandRunnerStub{}
	artifactPath := "releases/r1/desktop/linux-amd64/client.tar.gz"
	installer := LinuxInstaller{Stager: BundleStager{Fetcher: artifactFetcher{artifactPath: bundle}, Root: t.TempDir()}, Paths: paths, Runner: runner}
	if err := installer.Apply(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 || filepath.Base(runner.calls[0].program) != "install-user.sh" || runner.calls[0].directory != filepath.Dir(runner.calls[0].program) {
		t.Fatalf("runner calls = %+v", runner.calls)
	}
	env := strings.Join(runner.calls[0].environment, "\n")
	for _, wanted := range []string{"HOME=" + home, "PREFIX=" + paths.Prefix, "ENABLE_DAEMON=0", "ENABLE_AUTOSTART=0", "RESTART_DAEMON=0"} {
		if !strings.Contains(env, wanted) {
			t.Errorf("installer environment missing %q: %s", wanted, env)
		}
	}
	if _, err := os.Stat(runner.calls[0].directory); !os.IsNotExist(err) {
		t.Fatalf("staging survived apply: %v", err)
	}
}

func TestLinuxInstallerRejectsIncompleteBundleAndRelativeInstallRoot(t *testing.T) {
	bundle := makeBundle(t, tarEntry{name: "install-user.sh", typeflag: 0, mode: 0o755, data: "#!/bin/sh\n"})
	resolved := stagedRelease(bundle)
	artifactPath := "releases/r1/desktop/linux-amd64/client.tar.gz"
	installer := LinuxInstaller{Stager: BundleStager{Fetcher: artifactFetcher{artifactPath: bundle}, Root: t.TempDir()}, Paths: LinuxPaths{Home: "/home/u", Prefix: "relative", DataHome: "/data", ConfigHome: "/config"}}
	if _, _, err := installer.Plan(context.Background(), resolved); err == nil {
		t.Fatal("accepted incomplete bundle or relative prefix")
	}
}
