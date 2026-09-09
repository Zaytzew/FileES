package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/internal/nativeruntime"
)

func TestRefusesInvalidInputBeforeOutput(t *testing.T) {
	stage := t.TempDir()
	out := filepath.Join(t.TempDir(), "output")
	if err := run(t.TempDir(), stage, out); err == nil {
		t.Fatal("accepted empty runtime")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("touched output for invalid runtime")
	}
}

func TestStageMustMatchCurrentNativeSources(t *testing.T) {
	source, stage := t.TempDir(), t.TempDir()
	native := filepath.Join(source, "native", "filees-svn")
	if err := os.MkdirAll(filepath.Join(native, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stage, "notices"), 0700); err != nil {
		t.Fatal(err)
	}
	var list []map[string]string
	for _, name := range []string{"CMakeLists.txt", "src/main.c"} {
		if err := os.WriteFile(filepath.Join(native, filepath.FromSlash(name)), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		list = append(list, map[string]string{"name": name, "sha256": nativeruntime.ID([]byte(name))})
	}
	raw, err := json.Marshal(map[string]any{"sources": list})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "notices", "DEPENDENCIES.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySources(source, stage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "src", "main.c"), []byte("new implementation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySources(source, stage); err == nil {
		t.Fatal("accepted stale C stage")
	}
	if err := os.WriteFile(filepath.Join(native, "src", "main.c"), []byte("src/main.c"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "src", "extra.c"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySources(source, stage); err == nil {
		t.Fatal("accepted changed C source set")
	}
}
