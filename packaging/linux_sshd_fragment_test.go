package packaging_test

import (
	"os"
	"strings"
	"testing"
)

// The Linux sshd fragment is the OpenBSD one with a single difference: the
// tunnel account authenticates through PAM instead of BSD Authentication.
// Every other account, option and comment must stay identical, so a hardening
// change made for OpenBSD cannot silently miss Linux.
func TestLinuxSSHDFragmentDiffersFromOpenBSDOnlyInTunnelAuthentication(t *testing.T) {
	read := func(path string) []string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	}
	openbsd, linux := read("server/openbsd/filees.conf"), read("server/linux/filees.conf")

	var onlyOpenBSD, onlyLinux []string
	seen := map[string]int{}
	for _, line := range openbsd {
		seen[line]++
	}
	for _, line := range linux {
		if seen[line] > 0 {
			seen[line]--
			continue
		}
		onlyLinux = append(onlyLinux, strings.TrimSpace(line))
	}
	for line, n := range seen {
		for ; n > 0; n-- {
			onlyOpenBSD = append(onlyOpenBSD, strings.TrimSpace(line))
		}
	}
	if len(onlyOpenBSD) != 2 {
		t.Fatalf("lines only in the OpenBSD fragment: %q", onlyOpenBSD)
	}
	for _, line := range onlyOpenBSD {
		if !strings.Contains(line, "bsdauth") && !strings.Contains(line, "BSD Auth challenge") {
			t.Fatalf("unexpected OpenBSD-only line: %q", line)
		}
	}
	joined := strings.Join(onlyLinux, "\n")
	for _, want := range []string{"AuthenticationMethods keyboard-interactive:pam", "PAMServiceName filees-tunnel"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Linux fragment lacks %q; Linux-only lines: %q", want, onlyLinux)
		}
	}
	for _, line := range onlyLinux {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "AuthenticationMethods keyboard-interactive:pam") || line == "PAMServiceName filees-tunnel" {
			continue
		}
		t.Fatalf("unexpected Linux-only directive: %q", line)
	}
}

// The PAM service must name the module by absolute path (the module directory
// differs between Debian and Fedora) and must not pull in the system stacks.
func TestLinuxTunnelPAMServiceUsesOnlyTheFileESModule(t *testing.T) {
	raw, err := os.ReadFile("server/linux/filees-tunnel.pam")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "requisite" || fields[2] != "/usr/local/libexec/filees/pam_filees_tunnel.so" {
			t.Fatalf("unexpected PAM line: %q", line)
		}
		kinds[fields[0]] = true
	}
	for _, kind := range []string{"auth", "account", "session"} {
		if !kinds[kind] {
			t.Fatalf("PAM service has no %s line", kind)
		}
	}
}
