//go:build !windows

package servertool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"filees/internal/obsandbox"
	"filees/pkg/serverconfig"
)

func TestLoadSpoolConfigurationAndCapacityPath(t *testing.T) {
	path, _, _ := writeRepoPruneFixtureConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	if err = json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	repo := conf["repositories"].(map[string]any)
	spool := filepath.Join(t.TempDir(), "spool")
	for _, tc := range []struct {
		path  string
		max   int64
		valid bool
	}{
		{"", 0, true}, {spool, 1024, true}, {"relative", 0, false}, {"/", 0, false}, {spool, -1, false}, {spool + "\n", 0, false},
	} {
		repo["load_spool_root"], repo["max_dump_size"] = tc.path, tc.max
		raw, err = json.Marshal(conf)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := serverconfig.LoadFor(path, 0)
		if (err == nil) != tc.valid {
			t.Fatalf("%q max=%d: %v", tc.path, tc.max, err)
		}
		if tc.valid && tc.path != "" {
			if got.Repositories.MaxDumpSize != tc.max || got.Repositories.EffectiveLoadSpoolRoot() != spool {
				t.Fatal("settings lost")
			}
			found := false
			for _, p := range capacityPaths(got) {
				if p.Label == "load-spool" && p.Name == spool {
					found = true
				}
			}
			if !found {
				t.Fatal("capacity monitor omits custom spool")
			}
		}
	}
}

func TestLoadSpoolSeparateUnveil(t *testing.T) {
	root := os.Getenv("FILEES_LOAD_SPOOL_NATIVE_ROOT")
	if root == "" {
		root = t.TempDir()
		if runtime.GOOS == "openbsd" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoadSpoolSeparateUnveil$")
			cmd.Env = append(os.Environ(), "FILEES_LOAD_SPOOL_NATIVE_ROOT="+root)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native spool sandbox: %v: %s", err, out)
			}
			return
		}
	}
	results, spool := filepath.Join(root, "results"), filepath.Join(root, "separate-spool")
	if err := os.Mkdir(results, 0700); err != nil {
		t.Fatal(err)
	}
	r := serverconfig.RepositoryFile{ResultsRoot: results}
	if len(repositoryLoadSpoolPaths(r)) != 0 {
		t.Fatal("default spool widens unveil")
	}
	r.LoadSpoolRoot = spool
	// A separately unveiled directory must exist when the profile is locked.
	// Creating only its last path component afterwards does not grant access
	// to children on OpenBSD. The operator prepares an explicit spool root.
	if err := os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}
	paths := repositoryLoadSpoolPaths(r)
	if len(paths) != 1 || paths[0].Name != spool || paths[0].Perms != "rwc" {
		t.Fatalf("wrong spool paths: %v", paths)
	}
	paths = append(paths, obsandbox.Path{Label: "results", Name: results, Perms: "rwc"})
	if err := obsandbox.Apply(obsandbox.Profile{Name: "load-spool-test", Promises: writePromises, Paths: paths}); err != nil {
		t.Fatal(err)
	}
	attempt, err := os.MkdirTemp(spool, "load-*")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(attempt, "carrier.dump"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(attempt); err != nil {
		t.Fatal(err)
	}
}
