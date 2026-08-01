//go:build openbsd

package obsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNarrowAfterLockedUnveil(t *testing.T) {
	if os.Getenv("FILEES_NARROW_NATIVE") == "1" {
		path := os.Getenv("FILEES_NARROW_PATH")
		if err := Begin("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		profile := Profile{
			Name:     "obsandbox/narrow-test",
			Promises: "stdio rpath",
			Paths:    []Path{{Label: "probe", Name: path, Perms: "r"}},
		}
		if err := Apply(profile); err != nil {
			t.Fatal(err)
		}
		if err := Narrow("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(path); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, []byte("locked unveil remains usable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestNarrowAfterLockedUnveil$")
	command.Env = append(os.Environ(), "FILEES_NARROW_NATIVE=1", "FILEES_NARROW_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native narrow child: %v: %s", err, output)
	}
}
