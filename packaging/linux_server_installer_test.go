package packaging

import (
	"os"
	"regexp"
	"strings"
	"testing"
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
