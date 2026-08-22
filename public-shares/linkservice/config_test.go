package linkservice

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFixture(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	key := filepath.Join(root, "visit.key")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body = strings.ReplaceAll(body, "@ROOT@", root)
	body = strings.ReplaceAll(body, "@KEY@", key)
	path := filepath.Join(root, "public-links.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadContainsNoRepositoryOrCredentialSurface(t *testing.T) {
	path := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"unix","address":"@ROOT@/fcgi.sock","socket_group":"www"},"backchannel":{"network":"unix","address":"@ROOT@/authority.sock"},"visit_key_file":"@KEY@","cache":{"enabled":true,"root":"@ROOT@/cache","ttl":"12h","max_size":1048576}}`)
	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.CacheTTL.Hours() != 12 {
		t.Fatalf("ttl=%v", runtime.CacheTTL)
	}
	if runtime.BundleMaxFiles != 512 || runtime.BundleMaxSize != 1048576 {
		t.Fatalf("bundle defaults files=%d size=%d", runtime.BundleMaxFiles, runtime.BundleMaxSize)
	}
	if _, err := os.Stat(runtime.Config.Cache.Root); err != nil {
		t.Fatalf("cache root not prepared: %v", err)
	}
	raw, _ := os.ReadFile(path)
	for _, forbidden := range []string{`"svn`, `"repository_root"`, `"private_key`, `"password`} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("public config contains %q: %s", forbidden, raw)
		}
	}
}

func TestLoadRejectsBundleWithoutCacheOrBeyondCache(t *testing.T) {
	withoutCache := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"tcp","address":"127.0.0.1:9000"},"backchannel":{"network":"tcp","address":"127.0.0.1:9001"},"visit_key_file":"@KEY@","cache":{"enabled":false},"bundle":{"max_files":10,"max_size":1024}}`)
	if _, err := Load(withoutCache); err == nil {
		t.Fatal("bundle without private cache accepted")
	}
	beyondCache := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"tcp","address":"127.0.0.1:9000"},"backchannel":{"network":"tcp","address":"127.0.0.1:9001"},"visit_key_file":"@KEY@","cache":{"enabled":true,"root":"@ROOT@/cache","max_size":1024},"bundle":{"max_files":10,"max_size":2048}}`)
	if _, err := Load(beyondCache); err == nil {
		t.Fatal("bundle larger than cache accepted")
	}
}

func TestLoadPreparesOptionalIntakeRoot(t *testing.T) {
	path := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"unix","address":"@ROOT@/fcgi.sock"},"backchannel":{"network":"unix","address":"@ROOT@/authority.sock"},"visit_key_file":"@KEY@","cache":{"enabled":false},"intake_root":"@ROOT@/intake","max_upload_size":4096}`)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0022 != 0 {
		t.Skip("host file mode cannot satisfy public-links owner-only config")
	}
	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Intake == nil || runtime.Intake.MaxBytes != 4096 {
		t.Fatalf("intake=%+v", runtime.Intake)
	}
	if _, err := os.Stat(runtime.Config.IntakeRoot); err != nil {
		t.Fatal(err)
	}
	sameCache := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"tcp","address":"127.0.0.1:9000"},"backchannel":{"network":"tcp","address":"127.0.0.1:9001"},"visit_key_file":"@KEY@","cache":{"enabled":true,"root":"@ROOT@/cache","max_size":1024},"intake_root":"@ROOT@/cache"}`)
	if _, err := Load(sameCache); err == nil {
		t.Fatal("intake on cache root accepted")
	}
}

func TestLoadRejectsPublicBackchannelAndUnknownFields(t *testing.T) {
	publicTCP := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"tcp","address":"127.0.0.1:9000"},"backchannel":{"network":"tcp","address":"0.0.0.0:9001"},"visit_key_file":"@KEY@","cache":{"enabled":false}}`)
	if _, err := Load(publicTCP); err == nil {
		t.Fatal("public backchannel accepted")
	}
	unknown := writeConfigFixture(t, `{"schema":"filees.public-links/v1","fastcgi":{"network":"tcp","address":"127.0.0.1:9000"},"backchannel":{"network":"tcp","address":"127.0.0.1:9001"},"visit_key_file":"@KEY@","cache":{"enabled":false},"repository_root":"/secret"}`)
	if _, err := Load(unknown); err == nil {
		t.Fatal("repository field accepted by public config")
	}
}
