package packaging_test

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/gui/identity"
)

func TestLinuxDesktopMetadata(t *testing.T) {
	data, err := os.ReadFile("linux/filees-gui.desktop")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"[Desktop Entry]", "Type=Application", "Name=" + identity.Name,
		"Exec=/usr/local/bin/filees-gui", "Icon=" + identity.ID, "Terminal=false",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("desktop metadata missing %q", required)
		}
	}
}

func TestLinuxUserInstallerIsExplicitAboutAutostart(t *testing.T) {
	installScript, err := os.ReadFile("linux/install-user.sh")
	if err != nil {
		t.Fatal(err)
	}
	uninstallScript, err := os.ReadFile("linux/uninstall-user.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installScript), `ENABLE_AUTOSTART:-0`) {
		t.Fatal("user installer must not enable autostart without explicit opt-in")
	}
	for _, required := range []string{"bin/filees", "filees.service", "config-check", `ENABLE_DAEMON:-0`, `RESTART_DAEMON:-1`} {
		if !strings.Contains(string(installScript), required) {
			t.Errorf("installer missing %q", required)
		}
	}
	for _, required := range []string{"bin/filees", "filees-gui.desktop", "filees-gui.svg", "autostart/filees-gui.desktop", "filees.service", "preserved"} {
		if !strings.Contains(string(uninstallScript), required) {
			t.Errorf("uninstaller does not remove %q", required)
		}
	}
}

func TestLinuxSystemdUnitHasBoundedGracefulLifecycle(t *testing.T) {
	raw, err := os.ReadFile("linux/filees.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		`ExecStart="@FILEES_BIN@" daemon --config "@CONFIG_PATH@"`,
		"Restart=on-failure", "TimeoutStopSec=15min", "UMask=0077", "WantedBy=default.target",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("systemd unit missing %q", required)
		}
	}
}

func TestLinuxInstallUpgradeUninstallLifecyclePreservesConfig(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	home := filepath.Join(root, "home")
	prefix := filepath.Join(home, ".local")
	dataHome := filepath.Join(home, ".local", "share")
	configHome := filepath.Join(home, ".config")
	fakeBin := filepath.Join(root, "fake-bin")
	for _, dir := range []string{
		filepath.Join(bundle, "bin"), filepath.Join(bundle, "share", "applications"),
		filepath.Join(bundle, "share", "icons", "hicolor", "scalable", "apps"),
		filepath.Join(bundle, "share", "systemd", "user"), filepath.Join(bundle, "share", "filees"), fakeBin,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyTestFile(t, "linux/install-user.sh", filepath.Join(bundle, "install-user.sh"), 0o755)
	copyTestFile(t, "linux/uninstall-user.sh", filepath.Join(bundle, "uninstall-user.sh"), 0o755)
	copyTestFile(t, "linux/filees-gui.desktop", filepath.Join(bundle, "share", "applications", "filees-gui.desktop"), 0o644)
	copyTestFile(t, "linux/filees.service", filepath.Join(bundle, "share", "systemd", "user", "filees.service"), 0o644)
	copyTestFile(t, "linux/config.example.json", filepath.Join(bundle, "share", "filees", "config.example.json"), 0o644)
	if err := os.WriteFile(filepath.Join(bundle, "share", "icons", "hicolor", "scalable", "apps", "filees-gui.svg"), []byte("<svg/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileesStub := "#!/bin/sh\n[ \"$1\" = config-check ] || exit 2\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bundle, "bin", "filees"), []byte(fileesStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "bin", "filees-gui"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\n[ \"$2\" = is-active ] && exit 1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	checksum := exec.Command("sh", "-c", "find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | xargs sha256sum > SHA256SUMS")
	checksum.Dir = bundle
	if output, err := checksum.CombinedOutput(); err != nil {
		t.Fatalf("create test checksum manifest: %v\n%s", err, output)
	}
	env := append(os.Environ(),
		"HOME="+home, "PREFIX="+prefix, "XDG_DATA_HOME="+dataHome, "XDG_CONFIG_HOME="+configHome,
		"PATH="+fakeBin+":/usr/bin:/bin", "ENABLE_DAEMON=0", "ENABLE_AUTOSTART=0",
	)
	runScript(t, filepath.Join(bundle, "install-user.sh"), env)
	configPath := filepath.Join(configHome, "filees", "config.json")
	custom := []byte("[{\"managed\":true}]\n")
	if err := os.WriteFile(configPath, custom, 0o600); err != nil {
		t.Fatal(err)
	}
	runScript(t, filepath.Join(bundle, "install-user.sh"), env)
	if got, err := os.ReadFile(configPath); err != nil || !stringEqual(got, custom) {
		t.Fatalf("upgrade overwrote config: %q err=%v", got, err)
	}
	for _, path := range []string{
		filepath.Join(prefix, "bin", "filees"), filepath.Join(prefix, "bin", "filees-gui"),
		filepath.Join(configHome, "systemd", "user", "filees.service"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("installed artifact %s: %v", path, err)
		}
	}
	runScript(t, filepath.Join(bundle, "uninstall-user.sh"), env)
	if _, err := os.Stat(filepath.Join(prefix, "bin", "filees")); !os.IsNotExist(err) {
		t.Fatalf("daemon binary survived uninstall: %v", err)
	}
	if got, err := os.ReadFile(configPath); err != nil || !stringEqual(got, custom) {
		t.Fatalf("uninstall removed config: %q err=%v", got, err)
	}
}

func copyTestFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func runScript(t *testing.T, path string, env []string) {
	t.Helper()
	cmd := exec.Command(path)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, output)
	}
}

func stringEqual(a, b []byte) bool { return string(a) == string(b) }

func TestWindowsManifestIsWellFormedAndUnelevated(t *testing.T) {
	data, err := os.ReadFile("windows/filees-gui.exe.manifest")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("invalid Windows manifest: %v", err)
	}
	text := string(data)
	for _, required := range []string{`level="asInvoker"`, "PerMonitorV2", "longPathAware"} {
		if !strings.Contains(text, required) {
			t.Errorf("Windows manifest missing %q", required)
		}
	}
}

func TestWindowsPackagingIdentityMatchesApplication(t *testing.T) {
	data, err := os.ReadFile("windows/identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Name  string `json:"name"`
		AUMID string `json:"app_user_model_id"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Name != identity.Name || metadata.AUMID != identity.AUMID {
		t.Fatalf("packaging identity = %#v, application = %q/%q", metadata, identity.Name, identity.AUMID)
	}
}

func TestWindowsBuildUsesGUISubsystem(t *testing.T) {
	data, err := os.ReadFile("build-gui.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `-H=windowsgui`) {
		t.Fatal("Windows GUI build would open a console window")
	}
}

func TestLinuxBuildContainsFullClientServiceAndChecksums(t *testing.T) {
	raw, err := os.ReadFile("build-gui.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"filees-client-linux-amd64", "./cmd/filees", "./cmd/filees-gui",
		"filees.service", "config.example.json", "SHA256SUMS", "sha256sum",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Linux client build missing %q", required)
		}
	}
}

func TestWindowsInstallerCreatesPerUserShortcutWithAUMID(t *testing.T) {
	data, err := os.ReadFile("windows/filees-gui.wxs")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatalf("invalid WiX source: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		`Scope="perUser"`, `Id="LocalAppDataFolder"`, `Id="ProgramMenuFolder"`,
		`Key="System.AppUserModel.ID"`, `Value="` + identity.AUMID + `"`,
		`Version="$(var.ProductVersion)"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("WiX source missing %q", required)
		}
	}
}

func TestWindowsMSIBuildScriptUsesWiX4(t *testing.T) {
	data, err := os.ReadFile("windows/build-msi.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"WiX Toolset v4", "wix build", `SourceDir=$source`, `ProductVersion=$productVersion`, `VERSION`} {
		if !strings.Contains(text, required) {
			t.Errorf("MSI build script missing %q", required)
		}
	}
}
