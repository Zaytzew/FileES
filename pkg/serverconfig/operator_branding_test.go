package serverconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOperatorBrandingOnlyResolvesForSMTPTools is a regression test for a
// live incident on cloud.atmprojekt.pl: resolving operator_branding
// unconditionally in load() meant every tool that loads server.json tried to
// read logo_file off disk, including sandboxed sessions
// (filees-client-entry's ClientControlCommand child, filees-worker deploy)
// that never touch mail and whose obsandbox profiles have no reason to
// unveil an operator's logo. Those sessions re-load server.json AFTER
// unveil is already locked by their parent, so the read failed with a
// misleading ENOENT - repository creation and realm-alias claim both broke
// the moment a custom logo_file was configured. OperatorBranding must only
// be resolved (and its logo_file only touched) when the caller actually
// asked for SecretSMTP, the same gate already used for the SMTP
// password/CA file just above it in load().
func TestOperatorBrandingOnlyResolvesForSMTPTools(t *testing.T) {
	root := t.TempDir()
	pepperFile := filepath.Join(root, "otp.pepper")
	if err := os.WriteFile(pepperFile, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="), 0o600); err != nil {
		t.Fatal(err)
	}
	svnBinary, svnserveBinary, svnadminBinary := "/bin/sh", "/bin/sh", "/bin/sh"
	activationRoot := filepath.Join(root, "activation")
	file := map[string]any{
		"schema": Schema, "root": filepath.Join(root, "onboarding"), "otp_pepper_file": pepperFile,
		"operation_ttl": "30m", "otp_attempts": 5, "reverse_port_first": 42000, "reverse_port_last": 42000,
		"activation": map[string]any{
			"root": activationRoot, "authorized_keys_file": filepath.Join(activationRoot, "authorized_keys"),
			"authz_file": filepath.Join(activationRoot, "authz"), "service_working_copy": filepath.Join(root, "wc"),
			"service_repository": filepath.Join(root, "repo"), "repository_name": "filees-service",
			"client_entry_path": "/usr/local/libexec/filees/filees-client-entry",
			"svn_binary":        svnBinary, "svnserve_binary": svnserveBinary,
		},
		"repositories": map[string]any{
			"root": filepath.Join(root, "repositories"), "results_root": filepath.Join(root, "results"),
			"data_authz_file": filepath.Join(activationRoot, "repositories.authz"), "svnadmin_binary": svnadminBinary,
			"url_prefix": "svn+ssh://_filees-data@filees.test/", "recovery_admin_contact": "filees@example.test",
		},
		"invitation": map[string]any{
			"server_id": "operator-branding-test", "server_address": "filees.test:2222",
			"known_host": "[filees.test]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
		"operator_branding": map[string]any{
			"logo_file": filepath.Join(root, "does-not-exist.png"),
		},
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFor(configPath, 0); err != nil {
		t.Fatalf("config load without SecretSMTP must not touch operator_branding.logo_file: %v", err)
	}
	if _, err := LoadFor(configPath, SecretSMTP); err == nil {
		t.Fatal("config load with SecretSMTP silently accepted a missing operator_branding.logo_file")
	}
}
