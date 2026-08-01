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
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
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
