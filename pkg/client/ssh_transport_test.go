package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedSSHEnvironmentHasOneExplicitTunnel(t *testing.T) {
	root := t.TempDir()
	identity, known := filepath.Join(root, "id"), filepath.Join(root, "known")
	env, err := PinnedSSHEnvironment([]string{"PATH=existing", "SVN_SSH=unsafe", "svn_ssh=unsafe2"}, identity, known, 2223, "host.example")
	if err != nil {
		t.Fatal(err)
	}
	var tunnel string
	count := 0
	for _, value := range env {
		key, val, _ := strings.Cut(value, "=")
		if strings.EqualFold(key, "SVN_SSH") {
			count++
			tunnel = val
		}
	}
	if count != 1 {
		t.Fatal(env)
	}
	for _, want := range []string{"StrictHostKeyChecking=yes", "IdentityAgent=none", "BatchMode=yes", "HostName=host.example", "2223", filepath.ToSlash(identity), filepath.ToSlash(known)} {
		if !strings.Contains(tunnel, want) {
			t.Fatalf("missing %s in %s", want, tunnel)
		}
	}
	for _, path := range []string{"", "relative", filepath.Join(root, "with space")} {
		if _, err := PinnedSSHEnvironment(nil, path, known, 2223, ""); err == nil {
			t.Fatal("accepted invalid identity", path)
		}
	}
}
