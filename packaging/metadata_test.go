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
	"filees/internal/releasepublish"
	serverconfig "filees/internal/serverinstall/config"
	"filees/pkg/config"
)

func TestOpenBSDServerBinaryPolicyMatchesPrivilegeBoundaries(t *testing.T) {
	spec, err := releasepublish.LoadSpec("server/openbsd-binary-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	byTarget := make(map[string]releasepublish.FileSpec, len(spec.Files))
	for _, file := range spec.Files {
		if _, exists := byTarget[file.Target]; exists {
			t.Fatalf("duplicate managed target %s", file.Target)
		}
		byTarget[file.Target] = file
	}
	want := map[string]releasepublish.FileSpec{
		"{libexec_dir}/filees/filees-bootstrap-entry": {Mode: "4511", Owner: "_filees-state", Group: "wheel"},
		"{libexec_dir}/filees/filees-entry":           {Mode: "4511", Owner: "_filees-state", Group: "wheel"},
		"{libexec_dir}/filees/filees-client-entry":    {Mode: "4550", Owner: "_filees-state", Group: "_filees-access"},
		"{libexec_dir}/filees/filees-mobile-v1":       {Mode: "4550", Owner: "_filees-state", Group: "_filees-access"},
		"{libexec_dir}/filees/filees-worker":               {Mode: "0555", Owner: "root", Group: "wheel"},
		"{libexec_dir}/filees/filees-service-wc-corrector": {Mode: "4550", Owner: "root", Group: "_filees-access"},
		"/usr/libexec/auth/login_-filees":                  {Mode: "4550", Owner: "_filees-state", Group: "auth"},
	}
	for target, expected := range want {
		got, ok := byTarget[target]
		if !ok || got.Mode != expected.Mode || got.Owner != expected.Owner || got.Group != expected.Group {
			t.Errorf("policy %s = %+v, want mode=%s owner=%s group=%s", target, got, expected.Mode, expected.Owner, expected.Group)
		}
	}
}

func TestManualHtdocsFitsStaticAllowlist(t *testing.T) {
	root := filepath.Join("..", "manual")
	allow := []string{"/", "/index.html", "/favicon.ico", "/robots.txt", "/.well-known/", "/assets/"}
	conf, err := os.ReadFile(filepath.Join("server", "openbsd", "public-links.httpd.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(conf)
	for _, loc := range []string{`location "/"`, `location "/index.html"`, `location "/favicon.ico"`, `location "/robots.txt"`, `location "/assets/*"`, `location "/*"`} {
		if !strings.Contains(text, loc) {
			t.Errorf("httpd example missing %s", loc)
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		urlPath := "/" + rel
		if rel == "index.html" {
			urlPath = "/index.html"
		}
		covered := false
		for _, prefix := range allow {
			if prefix == urlPath || prefix == "/" && urlPath == "/index.html" {
				covered = true
				break
			}
			if strings.HasSuffix(prefix, "/") && strings.HasPrefix(urlPath, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s would be served as %s, which is not on the static allowlist", rel, urlPath)
		}
		switch rel {
		case "index.html", "favicon.ico", "robots.txt":
		default:
			if !strings.HasPrefix(rel, "assets/") {
				t.Errorf("%s must live under assets/ or be a root allowlisted file", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerMandocPagesArePresent(t *testing.T) {
	root := filepath.Join("..", "docs", "man")
	want := []string{
		"man5/filees.conf.5",
		"man5/filees-public-links.5",
		"man7/filees.7",
		"man8/filees-admin.8",
		"man8/filees-install.8",
		"man8/filees-operation.8",
		"man8/filees-mail.8",
		"man8/filees-onboard.8",
		"man8/filees-rotate.8",
		"man8/filees-bootstrap-entry.8",
		"man8/filees-entry.8",
		"man8/filees-client-entry.8",
		"man8/filees-worker.8",
		"man8/filees-service-wc-corrector.8",
		"man8/filees-ssh-auth.8",
		"man8/filees-public-authority.8",
		"man8/filees-links.8",
		"man8/filees-mobile-v1.8",
		"man8/filees-recovery-entry.8",
	}
	for _, rel := range want {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		text := string(data)
		for _, required := range []string{".Dd ", ".Dt ", ".Sh NAME", ".Nd ", ".Sh DESCRIPTION"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing %q", rel, required)
			}
		}
	}
	script, err := os.ReadFile("server/install-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "share/man") {
		t.Fatal("install-server.sh must install bundled man pages")
	}
}

func TestServerInstallerExampleConfigLoads(t *testing.T) {
	cfg, err := serverconfig.Load("server/install.example.conf")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RepoURL != "svn://cloud.atmprojekt.pl/FILEES-BIN" || cfg.Channel != "stable" || !cfg.VerifySignature {
		t.Fatalf("unexpected installer example config: %+v", cfg)
	}
}

func TestLinuxExampleConfigPassesProductionLoader(t *testing.T) {
	repositories, err := config.Load("linux/config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 0 {
		t.Fatalf("example config repositories = %d, want 0", len(repositories))
	}
}

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
	if err := os.WriteFile(filepath.Join(bundle, "bin", "filees-pair-gui"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
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
		filepath.Join(prefix, "bin", "filees-pair-gui"),
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
	if _, err := os.Stat(filepath.Join(prefix, "bin", "filees-pair-gui")); !os.IsNotExist(err) {
		t.Fatalf("pairing helper binary survived uninstall: %v", err)
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
		"filees-release-bundle", `-output "$out.tar.gz"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Linux client build missing %q", required)
		}
	}
}

func TestServerBundleContainsControlAndPublicShareTools(t *testing.T) {
	raw, err := os.ReadFile("build-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"openbsd-amd64", "linux-amd64", "filees-admin filees-onboard filees-bootstrap-entry filees-operation filees-mail",
		"filees-ssh-auth filees-entry filees-worker filees-service-wc-corrector filees-client-entry filees-mobile-v1 filees-recovery-entry",
		"filees-public-authority filees-links",
		`"./cmd/$command"`, "SHA256SUMS", "sha256 -r",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("server build missing %q", required)
		}
	}
	installer, err := os.ReadFile("server/install-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	installerText := string(installer)
	for _, required := range []string{"700", "600", "openssl rand -base64 32", "ssh-keygen -q -t ed25519", "No daemon or rc.d service", "filees-public-authority", "filees-links", `install -m 0555 "$bundle/bin/filees-install"`} {
		if !strings.Contains(installerText, required) {
			t.Errorf("server installer missing %q", required)
		}
	}
	for _, forbidden := range []string{"systemctl", "rcctl", "sshd_config"} {
		if strings.Contains(installerText, forbidden) {
			t.Errorf("S1 installer unexpectedly configures %q", forbidden)
		}
	}
	openBSDInstaller, err := os.ReadFile("server/openbsd/install-ssh.sh")
	if err != nil {
		t.Fatal(err)
	}
	openBSDInstallerText := string(openBSDInstaller)
	for _, required := range []string{
		`-m 4511 "$bundle/bin/filees-bootstrap-entry"`,
		`-m 0555 "$bundle/bin/filees-onboard"`,
		`-m 4511 "$bundle/bin/filees-entry"`,
		`-m 0555 "$bundle/bin/filees-worker"`,
		`-o root -g "$client_access_group" -m 4550 "$bundle/bin/filees-service-wc-corrector"`,
		`-m 0555 "$bundle/bin/filees-public-authority"`,
		`-m 0555 "$bundle/bin/filees-links"`,
		`-o root -g wheel -m 0555 "$bundle/bin/filees-install"`,
		`-o root -g wheel -m 0555 "$bundle/bin/filees-rotate"`,
		`-m 4550 "$bundle/bin/filees-client-entry"`,
		`-g _filees-recovery -m 4550 "$bundle/bin/filees-recovery-entry"`,
		`-o root -g wheel -m 0555 "$bundle/bin/filees-recovery-entry" /usr/local/libexec/filees/filees-recovery-authorize`,
		`svnadmin create /var/filees/service-repo`,
	} {
		if !strings.Contains(openBSDInstallerText, required) {
			t.Errorf("OpenBSD installer missing privilege boundary %q", required)
		}
	}
	if strings.Contains(openBSDInstallerText, `-m 4511 "$bundle/bin/filees-worker"`) {
		t.Error("OpenBSD worker must not be set-id: pledge execpromises reject set-id images")
	}
	if strings.Contains(openBSDInstallerText, `-m 4511 "$bundle/bin/filees-onboard"`) {
		t.Error("OpenBSD onboard child must inherit the dispatcher's state UID, not be set-id")
	}
	sshPolicy, err := os.ReadFile("server/openbsd/filees.conf")
	if err != nil {
		t.Fatal(err)
	}
	clientPolicy := strings.SplitN(string(sshPolicy), "Match User _filees-client", 2)
	if !strings.Contains(string(sshPolicy), "ForceCommand /usr/local/libexec/filees/filees-bootstrap-entry") {
		t.Fatal("OpenBSD bootstrap Match block does not trigger bounded onboard+mail entry")
	}
	for _, required := range []string{"AuthorizedKeysFile none", "AuthorizedKeysCommand /bin/cat /var/filees/activation/authorized_keys", "AuthorizedKeysCommandUser _filees-state"} {
		if len(clientPolicy) != 2 || !strings.Contains(clientPolicy[1], required) {
			t.Fatalf("OpenBSD client Match block missing state-owned key reader %q", required)
		}
	}
	recoveryPolicy := strings.SplitN(string(sshPolicy), "Match User _filees-recovery", 2)
	for _, required := range []string{
		"AuthenticationMethods publickey",
		"AuthorizedKeysCommand /usr/local/libexec/filees/filees-recovery-authorize authorize %t %k",
		"AuthorizedKeysCommandUser _filees-state",
		"DisableForwarding yes",
		"PermitTTY no",
	} {
		if len(recoveryPolicy) != 2 || !strings.Contains(recoveryPolicy[1], required) {
			t.Fatalf("OpenBSD recovery Match block missing %q", required)
		}
	}
	configRaw, err := os.ReadFile("server/server.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(configRaw, &config); err != nil {
		t.Fatalf("invalid server example config: %v", err)
	}
	if config["schema"] != "filees.server-toolchain/v1" || config["root"] != "/var/filees/onboarding" {
		t.Fatalf("unexpected server config identity: %#v", config)
	}
	activationConfig, ok := config["activation"].(map[string]any)
	if !ok || activationConfig["service_repository"] != "/var/filees/service-repo" || activationConfig["svnserve_binary"] != "/usr/local/bin/svnserve" || activationConfig["authorized_keys_file"] != "/var/filees/activation/authorized_keys" {
		t.Fatalf("unexpected activation config: %#v", activationConfig)
	}
}

func TestPublicLinksHTTPDUsesExplicitStaticAllowlist(t *testing.T) {
	publicHTTPD, err := os.ReadFile("server/openbsd/public-links.httpd.conf")
	if err != nil {
		t.Fatal(err)
	}
	text := string(publicHTTPD)
	for _, required := range []string{
		`location "/.well-known/acme-challenge/*"`,
		`location "/"`,
		`location "/*"`,
		`fastcgi socket "/run/filees/filees-links.sock"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("OpenBSD public endpoint example missing %q", required)
		}
	}
	if strings.Contains(text, "location not found") {
		t.Error("OpenBSD public endpoint must not let document-root presence shadow FileES routes")
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
		`Version="$(var.ProductVersion)"`, `Id="FileESGUIManifest"`,
		`Id="FileESRegistry"`, `<ui:WixUI Id="WixUI_Minimal" />`,
		`WixUILicenseRtf`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("WiX source missing %q", required)
		}
	}
	license, err := os.ReadFile("windows/License.rtf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(license), `okre\'9clone`) || strings.Contains(string(license), `okre\'b6lone`) {
		t.Fatalf("Windows license does not encode Polish ś in CP1250: %q", license)
	}
}

func TestWindowsMSIBuildScriptUsesWiX4(t *testing.T) {
	data, err := os.ReadFile("windows/build-msi.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"build $Wxs", "WixToolset.UI.wixext",
		`SourceDir=$staging`, `ProductVersion=$msiVersion`,
		`BundleVersion=$bundleVersion`, `VERSION`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("MSI build script missing %q", required)
		}
	}
	// The toolset major version is checked by the script, because getting it
	// wrong fails in a way that names anything but the cause: WiX 6 and 7
	// refuse to run until somebody accepts the Open Source Maintenance Fee
	// licence, and report that instead of anything about the package. WiX 4 and
	// 5 are MIT and read the same v4 schema, so the .wxs is unaffected either
	// way - which is exactly why nothing else would notice the difference.
	if !strings.Contains(text, "WIX7015") && !strings.Contains(text, "Open Source Maintenance Fee") {
		t.Error("MSI build script does not explain the WiX 6/7 licence refusal, which is what a wrong version actually reports")
	}
	if !strings.Contains(text, `$wixMajor -lt 4 -or $wixMajor -gt 5`) {
		t.Error("MSI build script does not bound the WiX major version it supports")
	}
	// wix is a dotnet global tool and is routinely absent from PATH in a fresh
	// shell, and after a per-user SDK install it resolves its runtime from the
	// machine-wide directory and refuses to start. Both are handled here rather
	// than left to whoever runs the build.
	if !strings.Contains(text, "DOTNET_ROOT") {
		t.Error("MSI build script does not point wix at a per-user dotnet runtime")
	}
	if !strings.Contains(text, `$cachedUIExtension`) || !strings.Contains(text, `-ext $uiExtension`) {
		t.Error("MSI build script does not resolve WiX 5's globally cached UI extension by exact DLL path")
	}
}
