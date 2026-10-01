package nativeruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFilesFixture(t *testing.T, text string) (string, []byte) {
	t.Helper()
	raw, files, err := BuildFileManifest(fixture(t, text))
	if err != nil {
		t.Fatal(err)
	}
	root := FilesRoot(t.TempDir(), raw)
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root, raw
}
func TestFilesInventoryIsDeterministicAndNoExecutablePayload(t *testing.T) {
	raw, _, err := BuildFileManifest(fixture(t, "unique-helper-executable-marker"))
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := BuildFileManifest(fixture(t, "unique-helper-executable-marker"))
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("not deterministic")
	}
	if bytes.Contains(raw, []byte("unique-helper-executable-marker")) {
		t.Fatal("inventory contains executable bytes")
	}
}
func TestInstalledFilesReadOnlyRefusal(t *testing.T) {
	for _, mode := range []string{"missing", "tamper", "extra", "directory"} {
		t.Run(mode, func(t *testing.T) {
			root, raw := writeFilesFixture(t, "helper")
			if _, err := VerifyFiles(root, raw); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, Executable)
			switch mode {
			case "missing":
				os.Remove(path)
			case "tamper":
				os.WriteFile(path, []byte("tamper"), 0600)
			case "extra":
				os.WriteFile(filepath.Join(root, "injected.dll"), []byte("extra"), 0600)
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0700)
			}
			if _, err := VerifyFiles(root, raw); err == nil {
				t.Fatal("accepted invalid installed runtime")
			}
		})
	}
}
func TestPublishFilesOldVersionsAndRollbackRemainIntact(t *testing.T) {
	old, oldRaw := writeFilesFixture(t, "old")
	next, nextRaw := writeFilesFixture(t, "next")
	install := t.TempDir()
	for _, v := range []struct {
		root string
		raw  []byte
	}{{old, oldRaw}, {next, nextRaw}, {old, oldRaw}} {
		if err := PublishFiles(v.root, install, v.raw); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyFiles(FilesRoot(install, v.raw), v.raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := VerifyFiles(FilesRoot(install, oldRaw), oldRaw); err != nil {
		t.Fatal("overwrote old runtime", err)
	}
	path := filepath.Join(FilesRoot(install, nextRaw), Executable)
	os.WriteFile(path, []byte("corrupt"), 0600)
	if err := PublishFiles(next, install, nextRaw); err == nil {
		t.Fatal("repaired published runtime in place")
	}
}
func TestFilesManifestRejectsPathsDuplicatesUnknownAndMissing(t *testing.T) {
	_, raw := writeFilesFixture(t, "helper")
	for _, mode := range []string{"traversal", "casefold", "missing", "unknown", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			var m FileManifest
			json.Unmarshal(raw, &m)
			var invalid []byte
			switch mode {
			case "traversal":
				m.Files[0].Name = "../bad.exe"
			case "casefold":
				m.Files = append(m.Files, FileDigest{"TEST.dll", m.Files[0].SHA256, 1})
			case "missing":
				m.Files = m.Files[1:]
			case "unknown":
				invalid = bytes.Replace(raw, []byte(`"schema":`), []byte(`"unknown":0,"schema":`), 1)
			case "trailing":
				invalid = append(bytes.Clone(raw), []byte(`{}`)...)
			}
			if invalid == nil {
				invalid, _ = json.Marshal(m)
			}
			if _, err := ParseFileManifest(invalid); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}
