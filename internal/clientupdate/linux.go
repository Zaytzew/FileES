//go:build !windows

package clientupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"filees/internal/releaseenvelope"
	contract "filees/pkg/contract/v1"
)

type LinuxPaths struct {
	Home       string
	Prefix     string
	DataHome   string
	ConfigHome string
}

type CommandRunner interface {
	Run(context.Context, string, string, []string) error
}

type ExecRunner struct{ Env []string }

func (runner ExecRunner) Run(ctx context.Context, program, directory string, environment []string) error {
	command := exec.CommandContext(ctx, program)
	command.Dir = directory
	command.Env = append(append([]string(nil), runner.Env...), environment...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

type LinuxInstaller struct {
	Stager BundleStager
	Paths  LinuxPaths
	Runner CommandRunner
}

func (installer LinuxInstaller) Plan(ctx context.Context, resolved *releaseenvelope.Resolved) ([]contract.UpdateChange, bool, error) {
	staged, err := installer.Stager.Stage(ctx, resolved)
	if err != nil {
		return nil, false, err
	}
	defer staged.Remove()
	paths, err := installer.normalizedPaths()
	if err != nil {
		return nil, false, err
	}
	if err := validateLinuxBundle(staged.Root); err != nil {
		return nil, false, err
	}
	mappings := []struct{ source, target, detail string }{
		{"bin/filees", filepath.Join(paths.Prefix, "bin", "filees"), "daemon"},
		{"bin/filees-gui", filepath.Join(paths.Prefix, "bin", "filees-gui"), "GUI"},
		{"share/icons/hicolor/scalable/apps/filees-gui.svg", filepath.Join(paths.DataHome, "icons", "hicolor", "scalable", "apps", "filees-gui.svg"), "ikona aplikacji"},
	}
	changes := make([]contract.UpdateChange, 0, len(mappings)+3)
	for _, mapping := range mappings {
		action, err := compareFile(filepath.Join(staged.Root, filepath.FromSlash(mapping.source)), mapping.target)
		if err != nil {
			return nil, false, err
		}
		changes = append(changes, contract.UpdateChange{Action: action, Path: mapping.target, Detail: mapping.detail})
	}
	for _, generated := range []struct{ target, detail string }{
		{filepath.Join(paths.DataHome, "applications", "filees-gui.desktop"), "wpis launchera zostanie wygenerowany ponownie"},
		{filepath.Join(paths.ConfigHome, "systemd", "user", "filees.service"), "unit użytkownika zostanie wygenerowany ponownie"},
	} {
		action := "update"
		if _, err := os.Stat(generated.target); errors.Is(err, os.ErrNotExist) {
			action = "add"
		} else if err != nil {
			return nil, false, err
		}
		changes = append(changes, contract.UpdateChange{Action: action, Path: generated.target, Detail: generated.detail})
	}
	config := filepath.Join(paths.ConfigHome, "filees", "config.json")
	if _, err := os.Stat(config); errors.Is(err, os.ErrNotExist) {
		changes = append(changes, contract.UpdateChange{Action: "add", Path: config, Detail: "nowa konfiguracja startowa"})
	} else if err != nil {
		return nil, false, err
	} else {
		changes = append(changes, contract.UpdateChange{Action: "unchanged", Path: config, Detail: "istniejąca konfiguracja zostanie zachowana"})
	}
	return changes, true, nil
}

func (installer LinuxInstaller) Apply(ctx context.Context, resolved *releaseenvelope.Resolved) error {
	staged, err := installer.Stager.Stage(ctx, resolved)
	if err != nil {
		return err
	}
	defer staged.Remove()
	paths, err := installer.normalizedPaths()
	if err != nil {
		return err
	}
	if err := validateLinuxBundle(staged.Root); err != nil {
		return err
	}
	runner := installer.Runner
	if runner == nil {
		runner = ExecRunner{Env: os.Environ()}
	}
	environment := []string{
		"HOME=" + paths.Home, "PREFIX=" + paths.Prefix, "XDG_DATA_HOME=" + paths.DataHome,
		"XDG_CONFIG_HOME=" + paths.ConfigHome, "ENABLE_DAEMON=0", "ENABLE_AUTOSTART=0", "RESTART_DAEMON=0",
	}
	return runner.Run(ctx, filepath.Join(staged.Root, "install-user.sh"), staged.Root, environment)
}

func (installer LinuxInstaller) normalizedPaths() (LinuxPaths, error) {
	paths := installer.Paths
	for name, value := range map[string]*string{"home": &paths.Home, "prefix": &paths.Prefix, "data_home": &paths.DataHome, "config_home": &paths.ConfigHome} {
		*value = filepath.Clean(strings.TrimSpace(*value))
		if !filepath.IsAbs(*value) {
			return LinuxPaths{}, fmt.Errorf("linux installer %s must be absolute", name)
		}
	}
	return paths, nil
}

func validateLinuxBundle(root string) error {
	for _, required := range []string{
		"install-user.sh", "SHA256SUMS", "VERSION", "bin/filees", "bin/filees-gui",
		"share/icons/hicolor/scalable/apps/filees-gui.svg", "share/applications/filees-gui.desktop",
		"share/systemd/user/filees.service", "share/filees/config.example.json",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(required)))
		if err != nil {
			return fmt.Errorf("bundle missing %s: %w", required, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle entry %s is not a regular file", required)
		}
	}
	installer, err := os.Stat(filepath.Join(root, "install-user.sh"))
	if err != nil || installer.Mode().Perm()&0o111 == 0 {
		return errors.New("bundle install-user.sh is not executable")
	}
	return nil
}
