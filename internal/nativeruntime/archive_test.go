package nativeruntime

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func fixture(t *testing.T, text string) []byte {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "notices"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredNotices {
		if err := os.WriteFile(filepath.Join(root, "notices", name), []byte("license fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list := inventory{Schema: "filees.native-runtime/v1", Platform: "windows-amd64", Files: []inventoryFile{{Executable, ID([]byte(text))}, {"test.dll", ID([]byte("dll"))}}}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{Executable: text, "test.dll": "dll", "notices/FileES-LICENSE.txt": "license", "notices/DEPENDENCIES.json": string(raw)} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Pack(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pack(root)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("not deterministic: %v", err)
	}
	return a
}

func TestConcurrentVersionsRemainIndependent(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "native-svn")
	one, two := fixture(t, "old-helper"), fixture(t, "new-helper")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := one
			if i%2 == 1 {
				payload = two
			}
			if _, err := Ensure(cache, payload); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for _, item := range []struct {
		payload []byte
		text    string
	}{{one, "old-helper"}, {two, "new-helper"}} {
		path, err := Ensure(cache, item.payload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != item.text {
			t.Fatalf("runtime changed: %s %v", got, err)
		}
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 2 {
		t.Fatalf("staging leftovers: %v %v", entries, err)
	}
}

func TestCorruptionIsNotRepairedInPlace(t *testing.T) {
	for _, kind := range []string{"changed", "missing", "extra", "directory", "symlink", "cache-symlink"} {
		t.Run(kind, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "native-svn")
			payload := fixture(t, "helper")
			path, err := Ensure(cache, payload)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(path)
			switch kind {
			case "changed":
				err = os.WriteFile(path, []byte("broken"), 0600)
			case "missing":
				err = os.Remove(path)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "intruder.dll"), []byte("bad"), 0600)
			case "directory":
				err = os.Remove(path)
				if err == nil {
					err = os.Mkdir(path, 0700)
				}
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(filepath.Join(root, "test.dll"), path)
				}
			case "cache-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				err = os.Symlink(cache, alias)
				cache = alias
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Ensure(cache, payload); err == nil {
				t.Fatal("accepted damaged/aliased runtime")
			}
			if kind == "changed" {
				got, _ := os.ReadFile(path)
				if string(got) != "broken" {
					t.Fatal("modified runtime in place")
				}
			}
		})
	}
}

func TestRejectArchiveBeforeCreatingCache(t *testing.T) {
	baseline, err := unpack(fixture(t, "helper"))
	if err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"../escape"}, {"C:/evil.dll"}, {"notices/../../escape"}, {"test.dll", "TEST.dll"}, {"notices/CON"}, {"notices/name."}, {"other.exe"}, {Executable}} {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		// Complete baseline, otherwise an unrelated missing-file check could
		// make every path/duplicate negative pass without exercising its guard.
		for name, data := range baseline {
			f, _ := w.Create(name)
			f.Write(data)
		}
		for _, name := range names {
			f, _ := w.Create(name)
			f.Write([]byte("bad"))
		}
		w.Close()
		cache := filepath.Join(t.TempDir(), "untouched")
		if _, err := Ensure(cache, buf.Bytes()); err == nil {
			t.Fatalf("accepted %v", names)
		}
		if _, err := os.Lstat(cache); !os.IsNotExist(err) {
			t.Fatalf("created cache for invalid archive %v", names)
		}
	}
}

func TestInventoryRejectsShortenedAndMixedStages(t *testing.T) {
	baseline, err := unpack(fixture(t, "helper"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "changed", "extra", "invalid-inventory", "missing-notice"} {
		t.Run(mode, func(t *testing.T) {
			files := make(map[string][]byte)
			for name, data := range baseline {
				files[name] = data
			}
			switch mode {
			case "missing-notice":
				delete(files, "notices/Subversion-LICENSE.txt")
			case "missing":
				delete(files, "test.dll")
			case "changed":
				files["test.dll"] = []byte("different-build")
			case "extra":
				files["extra.dll"] = []byte("unlisted")
			case "invalid-inventory":
				files["notices/DEPENDENCIES.json"] = []byte("{}")
			}
			var buf bytes.Buffer
			w := zip.NewWriter(&buf)
			for name, data := range files {
				f, _ := w.Create(name)
				f.Write(data)
			}
			w.Close()
			cache := filepath.Join(t.TempDir(), "untouched")
			if _, err := Ensure(cache, buf.Bytes()); err == nil {
				t.Fatal("accepted invalid inventory")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("created cache for invalid inventory")
			}
		})
	}
}
