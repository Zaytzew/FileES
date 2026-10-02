package packaging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"filees/internal/releasepublish"
	installmanifest "filees/internal/serverinstall/manifest"
)

func readLinuxServerFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The sshd fragment and the installer are two files describing one
// installation. An account sshd matches but nobody creates, or a program sshd
// starts but nobody installs, fails only at the first real login.
func TestLinuxInstallerProvidesEverythingTheSSHDFragmentNames(t *testing.T) {
	fragment := readLinuxServerFile(t, "server/linux/filees.conf")
	installer := readLinuxServerFile(t, "server/linux/install-ssh.sh")
	pam := readLinuxServerFile(t, "server/linux/filees-tunnel.pam")

	accounts := regexp.MustCompile(`(?m)^Match User (\S+)$`).FindAllStringSubmatch(fragment, -1)
	if len(accounts) != 6 {
		t.Fatalf("expected six SSH entry accounts, found %d", len(accounts))
	}
	for _, account := range accounts {
		if !strings.Contains(installer, "add_account "+account[1]+" ") {
			t.Errorf("sshd matches %s but the installer does not create it", account[1])
		}
	}
	// The state owner runs every helper; _filees-links owns the share cache.
	for _, service := range []string{`add_account "$state_user" `, "add_account _filees-links "} {
		if !strings.Contains(installer, service) {
			t.Errorf("installer does not create the service account: %s", service)
		}
	}
	programs := regexp.MustCompile(`/usr/local/libexec/filees/([A-Za-z0-9_.-]+)`).FindAllStringSubmatch(fragment+pam, -1)
	if len(programs) < 4 {
		t.Fatalf("expected the fragment and PAM service to name FileES programs, found %d", len(programs))
	}
	for _, program := range programs {
		if !strings.Contains(installer, `"$libexec/`+program[1]+`"`) {
			t.Errorf("sshd or PAM starts %s but the installer does not install it", program[1])
		}
	}
	// The PAM module runs this helper by its compiled-in default path.
	if !strings.Contains(installer, `"$libexec/filees-ssh-auth"`) {
		t.Error("installer does not install filees-ssh-auth where pam_filees_tunnel.so looks for it")
	}
	// A locked ('!') account is refused by sshd even for a valid key.
	if !strings.Contains(installer, `usermod -p '*' "$account"`) {
		t.Error("SSH entry accounts must have an unusable, not a locked, password field")
	}
	if strings.Contains(installer, "wheel") || strings.Contains(installer, "rcctl") || strings.Contains(installer, "cap_mkdb") {
		t.Error("Linux installer carries an OpenBSD-only command or group")
	}
}

// What the installer requires from the bundle, the builder must stage.
func TestLinuxServerBundleStagesWhatItsInstallerRequires(t *testing.T) {
	installer := readLinuxServerFile(t, "server/linux/install-ssh.sh")
	builder := readLinuxServerFile(t, "build-server.sh")
	for _, need := range []struct{ inInstaller, inBuilder string }{
		{`"$bundle/lib/pam_filees_tunnel.so"`, `"$tmp/lib/pam_filees_tunnel.so"`},
		{`"$bundle/share/filees/linux/filees.conf"`, `"$root/packaging/server/linux/filees.conf" "$tmp/share/filees/linux/"`},
		{`"$bundle/share/filees/linux/filees-tunnel.pam"`, `"$root/packaging/server/linux/filees-tunnel.pam" "$tmp/share/filees/linux/"`},
		{`"$bundle/share/filees/linux/filees.cil"`, `"$root/packaging/server/linux/filees.cil" "$tmp/share/filees/linux/"`},
		{`"$bundle/share/filees/linux/bootstrap_authorized_keys"`, `bootstrap_authorized_keys" "$tmp/share/filees/linux/"`},
	} {
		if !strings.Contains(installer, need.inInstaller) {
			t.Errorf("installer no longer uses %s; update this test", need.inInstaller)
		}
		if !strings.Contains(builder, need.inBuilder) {
			t.Errorf("builder does not stage %s", need.inInstaller)
		}
	}
	for _, binary := range regexp.MustCompile(`"\$bundle/bin/([a-z0-9-]+)"`).FindAllStringSubmatch(installer, -1) {
		if !regexp.MustCompile(`for command in [^\n]*\b` + regexp.QuoteMeta(binary[1]) + `\b`).MatchString(builder) {
			t.Errorf("installer installs %s but the builder does not build it", binary[1])
		}
	}
	if !strings.Contains(builder, `"/usr/bin/svnserve"`) {
		t.Error("Linux bundle must not ship an example pointing at OpenBSD's /usr/local/bin")
	}
}

// Under SELinux three programs run inside the confined sshd domain before any
// login exists. Each path they use must carry a FileES type, and the installer
// must label exactly those paths; a path sshd reads that the policy does not
// cover fails only on a real Fedora, as "Authentication failure".
func TestLinuxSELinuxPolicyCoversWhatSSHDRunsBeforeLogin(t *testing.T) {
	policy := readLinuxServerFile(t, "server/linux/filees.cil")
	installer := readLinuxServerFile(t, "server/linux/install-ssh.sh")
	fragment := readLinuxServerFile(t, "server/linux/filees.conf")
	for _, labelled := range []string{"/var/filees/onboarding", "/var/filees/activation", "/var/filees/repository-operations/recovery-keys"} {
		if !strings.Contains(policy, `(filecon "`+labelled+`(/.*)?"`) {
			t.Errorf("policy does not label %s", labelled)
		}
		if !regexp.MustCompile(`restorecon -R [^\n]*` + regexp.QuoteMeta(labelled) + `(\s|$)`).MatchString(installer) {
			t.Errorf("installer does not relabel %s", labelled)
		}
	}
	// The key file sshd reads lives in the labelled activation directory.
	for _, command := range regexp.MustCompile(`AuthorizedKeysCommand /bin/cat (\S+)`).FindAllStringSubmatch(fragment, -1) {
		if !strings.HasPrefix(command[1], "/var/filees/activation/") {
			t.Errorf("sshd reads %s, which the policy does not label", command[1])
		}
	}
	// A directory created later would take the generic /var type.
	if !strings.Contains(installer, "-m 700 /var/filees/repository-operations/recovery-keys") {
		t.Error("installer must create recovery-keys before labelling it")
	}
	for _, domain := range []string{"sshd_session_t", "sshd_t"} {
		if !regexp.MustCompile(`\(allow `+domain+` filees_onboarding_t \(file \([^)]*\bwrite\b`).MatchString(policy) ||
			!regexp.MustCompile(`\(allow `+domain+` filees_keys_t \(file \([^)]*\bread\b`).MatchString(policy) {
			t.Errorf("policy lacks the rules for %s", domain)
		}
		if regexp.MustCompile(`\(allow ` + domain + ` filees_keys_t \(file \([^)]*\bwrite\b`).MatchString(policy) {
			t.Errorf("%s must only read the key directories", domain)
		}
	}
	if !strings.Contains(installer, "selinuxenabled") || !strings.Contains(installer, `semodule -i "$bundle/share/filees/linux/filees.cil"`) {
		t.Error("installer does not load the policy where SELinux is enabled")
	}
}

// The updater's policy and the installer describe the same files. If they
// disagree on owner, group or mode, the first update changes permissions the
// installer set on purpose, and --adopt refuses a fresh installation.
func TestLinuxReleasePolicyMatchesWhatTheInstallerInstalls(t *testing.T) {
	spec, err := releasepublish.LoadSpec("server/linux-binary-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Platform != "linux-amd64" {
		t.Fatalf("platform=%q", spec.Platform)
	}
	installer := readLinuxServerFile(t, "server/linux/install-ssh.sh")
	expand := strings.NewReplacer(`"$state_user"`, "_filees-state", `"$client_access_group"`, "_filees-access",
		`"$libexec/`, `"/usr/local/libexec/filees/`)
	type installed struct{ owner, group, mode string }
	byTarget := map[string]installed{}
	line := regexp.MustCompile(`^install -o (\S+) -g (\S+) -m (\d+) "\$bundle/(\S+)" "?(/[^"\s]+)"?$`)
	for _, raw := range strings.Split(installer, "\n") {
		if m := line.FindStringSubmatch(expand.Replace(strings.TrimSpace(raw))); m != nil {
			byTarget[m[5]] = installed{m[1], m[2], strings.TrimLeft(m[3], "0")}
		}
	}
	if len(byTarget) < 18 {
		t.Fatalf("parsed only %d install lines from install-ssh.sh", len(byTarget))
	}
	targets := strings.NewReplacer("{sbin_dir}", "/usr/local/sbin", "{libexec_dir}", "/usr/local/libexec")
	checked := 0
	for _, file := range spec.Files {
		if file.Group == "wheel" || strings.Contains(file.Target, "/usr/libexec/auth/") {
			t.Errorf("%s carries an OpenBSD-only group or path: %+v", file.Source, file)
		}
		if strings.HasPrefix(file.Source, "share/") {
			continue // examples and manual pages come from install-server.sh
		}
		target := targets.Replace(file.Target)
		got, ok := byTarget[target]
		if !ok {
			t.Errorf("policy manages %s, which install-ssh.sh does not install", target)
			continue
		}
		if got.owner != file.Owner || got.group != file.Group || got.mode != strings.TrimLeft(file.Mode, "0") {
			t.Errorf("%s: installer %+v, policy %s:%s %s", target, got, file.Owner, file.Group, file.Mode)
		}
		delete(byTarget, target)
		checked++
	}
	if checked < 18 {
		t.Fatalf("compared only %d files", checked)
	}
	for target := range byTarget {
		if strings.HasPrefix(target, "/usr/local/") {
			t.Errorf("installer installs %s, which no update would ever replace", target)
		}
	}
}

// The Linux policy turns into a manifest the installer accepts, carries every
// manual page with the ownership install-server.sh leaves on Linux, and is
// what the release script uses for that platform.
func TestLinuxReleasePolicyGeneratesAnAcceptableManifest(t *testing.T) {
	spec, err := releasepublish.LoadSpec("server/linux-binary-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	payload := t.TempDir()
	pages := 0
	for _, file := range spec.Files {
		if strings.HasPrefix(file.Source, "share/man/") {
			pages++
			if file.Mode != "0444" || file.Owner != "root" || file.Group != "root" {
				t.Errorf("%s: %+v", file.Source, file)
			}
		}
		path := filepath.Join(payload, filepath.FromSlash(file.Source))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.Source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if pages != len(manPages(t)) {
		t.Fatalf("policy carries %d manual pages, docs/man has %d", pages, len(manPages(t)))
	}
	spec.ReleaseID, spec.Sequence, spec.SecurityEpoch = "r9999-server", 9999, 1
	raw, err := releasepublish.Generate(payload, spec)
	if err != nil {
		t.Fatal(err)
	}
	m, err := installmanifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Platform != "linux-amd64" {
		t.Fatalf("manifest platform=%q", m.Platform)
	}
	module := false
	for _, file := range m.Files {
		if file.Target == "{libexec_dir}/filees/pam_filees_tunnel.so" {
			module = true
		}
	}
	if !module {
		t.Fatal("the PAM module is not in the generated manifest")
	}
	// Found by the first real update on Debian: "*.so" is in Subversion's
	// default global-ignores, so a payload file with that name is signed in
	// the manifest but never added to FILEES-BIN.
	for _, file := range spec.Files {
		for _, pattern := range []string{"*.so", "*.so.[0-9]*", "*.a", "*.o", "*.la", "*.lo", "*.pyc", "*~"} {
			if ok, _ := filepath.Match(pattern, filepath.Base(file.Source)); ok {
				t.Errorf("payload source %s would be ignored by svn add", file.Source)
			}
		}
	}
	script := readLinuxServerFile(t, "../tools/prepare-server-release.sh")
	for _, need := range []string{`"packaging/server/${platform%-amd64}-binary-policy.json"`, `cp "$bundle/lib/pam_filees_tunnel.so" "$release_root/lib/pam_filees_tunnel.module"`, `openbsd-amd64|linux-amd64) ;;`, `svn would ignore these payload files`} {
		if !strings.Contains(script, need) {
			t.Errorf("prepare-server-release.sh lacks %s", need)
		}
	}
	if !strings.Contains(readLinuxServerFile(t, "build-server.sh"), `platform=linux-amd64`) {
		t.Error("Linux bundle must ship an install.conf example that follows the linux-amd64 manifest")
	}
}

// Scheduled maintenance is the administrator's choice between systemd timers
// and cron, with a keyboard-free equivalent. Both must run the same commands,
// and switching must leave exactly one scheduler behind.
func TestLinuxScheduledMaintenanceHasTwoEquivalentSchedulers(t *testing.T) {
	installer := readLinuxServerFile(t, "server/linux/install-ssh.sh")
	builder := readLinuxServerFile(t, "build-server.sh")
	jobs := map[string]string{
		"filees-passport-reap":       "/usr/local/libexec/filees/filees-worker passport-reap -config /etc/filees/server.json",
		"filees-passport-reap-check": "/usr/local/libexec/filees/filees-worker passport-reap -config /etc/filees/server.json --check",
		"filees-lock-requests-reap":  "/usr/local/sbin/filees-admin -config /etc/filees/server.json repo reap-lock-requests",
		"filees-capacity":            "/usr/local/sbin/filees-admin -config /etc/filees/server.json alert capacity --scheduled",
	}
	units, err := filepath.Glob("server/linux/systemd/*")
	if err != nil || len(units) != 2*len(jobs) {
		t.Fatalf("expected a service and a timer per job, found %d files (%v)", len(units), err)
	}
	cronCommands := strings.ReplaceAll(installer, "$libexec", "/usr/local/libexec/filees")
	for name, command := range jobs {
		service := readLinuxServerFile(t, "server/linux/systemd/"+name+".service")
		if !strings.Contains(service, "ExecStart="+command+"\n") || !strings.Contains(service, "User=_filees-state\n") || !strings.Contains(service, "Type=oneshot\n") {
			t.Errorf("%s.service does not run %q as the state user", name, command)
		}
		timer := readLinuxServerFile(t, "server/linux/systemd/"+name+".timer")
		if !strings.Contains(timer, "OnCalendar=") || !strings.Contains(timer, "WantedBy=timers.target") {
			t.Errorf("%s.timer is not an installable calendar timer", name)
		}
		if !strings.Contains(installer, name+".timer") {
			t.Errorf("installer does not enable %s.timer", name)
		}
		// The capacity entry is written by filees-install itself.
		if name != "filees-capacity" && !strings.Contains(cronCommands, command+" >/dev/null") {
			t.Errorf("cron variant does not run %q", command)
		}
	}
	for _, need := range []string{
		`scheduler=${FILEES_SCHEDULER:-}`, `if [ -t 0 ]; then`, `systemctl enable --now $timers`,
		`systemctl disable --now $timers`, `rm -f "/etc/systemd/system/$(basename "$unit")"`,
		`--ensure-capacity-cron`, `FILEES_SCHEDULER must be systemd, cron or none`,
	} {
		if !strings.Contains(installer, need) {
			t.Errorf("installer lacks %s", need)
		}
	}
	if !strings.Contains(builder, `packaging/server/linux/systemd/*.timer "$tmp/share/filees/linux/systemd/"`) {
		t.Error("builder does not stage the systemd units")
	}
}
