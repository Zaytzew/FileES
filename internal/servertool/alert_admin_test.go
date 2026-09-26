//go:build !windows

package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestAdminAlertPublicationWithSandbox(t *testing.T) {
	mucc, err := exec.LookPath("svnmucc")
	if err != nil {
		t.Skip("svnmucc unavailable")
	}
	configPath, _, _ := writeRepoPruneFixtureConfig(t)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	if err = json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	repoConfig := conf["repositories"].(map[string]any)
	admin := repoConfig["svnadmin_binary"].(string)
	if filepath.Dir(admin) != filepath.Dir(mucc) {
		tools := t.TempDir()
		if err = os.Symlink(admin, filepath.Join(tools, "svnadmin")); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(mucc, filepath.Join(tools, "svnmucc")); err != nil {
			t.Fatal(err)
		}
		repoConfig["svnadmin_binary"] = filepath.Join(tools, "svnadmin")
	}
	raw, _ = json.Marshal(conf)
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	realm := uuid.NewString()
	realmPath := filepath.Join(cfg.Activation.ServiceWorkingCopy, "admin", "realms", realm+".json")
	if err = os.MkdirAll(filepath.Dir(realmPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(realmPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-config", configPath, "alert", "publish", "--realm", realm, "--key", "disk.var", "--text", "Brak miejsca"}
	var out, stderr bytes.Buffer
	if code := runAdminInvocation(t, args, &out, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := runAdminInvocation(t, args, &out, &stderr); code != ExitOK || !strings.Contains(out.String(), "changed=false") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := runAdminInvocation(t, append(args, "--resolve"), &out, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
