//go:build windows && native_svn_package

package clientupdate

import (
	"archive/tar"
	"encoding/json"
	"filees/internal/nativeruntime"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in acceptance of the actual bundle through the production installer.
// No installed user processes
// or configuration are touched. Signature tests live in releaseenvelope.
func TestNativeBundleThroughDirectoryInstaller(t *testing.T) {
	bundle := os.Getenv("FILEES_TEST_NATIVE_BUNDLE")
	if !filepath.IsAbs(bundle) {
		t.Fatal("absolute FILEES_TEST_NATIVE_BUNDLE required")
	}
	var entries []tarEntry
	if err := filepath.WalkDir(bundle, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name, err := filepath.Rel(bundle, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, tarEntry{name: filepath.ToSlash(name), typeflag: tar.TypeReg, mode: 0755, data: string(data)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	archive := makeBundle(t, entries...)
	resolved := stagedRelease(archive)
	nativeRaw, readErr := os.ReadFile(filepath.Join(bundle, "bin", "native-svn.json"))
	if readErr == nil {
		resolved.Manifest.Artifacts[0].Kind = "bundle-native-files"
	}
	root := t.TempDir()
	installer := newInstaller(filepath.Join(root, "installed"), filepath.Join(root, "config.json"))
	installer.Stager = BundleStager{Fetcher: artifactFetcher{"releases/r1/desktop/linux-amd64/client.tar.gz": archive}, Root: root}
	if err := os.WriteFile(installer.Paths.ConfigPath, []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(t.Context(), resolved); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(installer.Paths.ConfigPath)
	if string(got) != "do not change" {
		t.Fatal("configuration changed")
	}
	// Only Windows itself can be found on PATH; no SDK/Tortoise borrowing.
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "cache"))
	t.Setenv("FILEES_NATIVE_SVN", "")
	exe := filepath.Join(installer.Paths.InstallDir, "filees.exe")
	out, err := exec.Command(exe, "native-runtime").CombinedOutput()
	if err != nil {
		t.Fatalf("first startup: %v %s", err, out)
	}
	helper := strings.TrimSpace(string(out))
	cacheRoot := filepath.Join(root, "cache", "FileES", "native-svn")
	if readErr == nil {
		want := filepath.Join(nativeruntime.FilesRoot(installer.Paths.InstallDir, nativeRaw), nativeruntime.Executable)
		if helper != want {
			t.Fatalf("not installed sidecar: %q want %q", helper, want)
		}
		if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
			t.Fatal("sidecar build created native cache", err)
		}
	} else {
		rel, err := filepath.Rel(cacheRoot, helper)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.Base(helper) != "filees-svn.exe" {
			t.Fatalf("not an embedded helper: %q %v", helper, err)
		}
	}
	out, err = exec.Command(helper, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("helper loader: %v %s", err, out)
	}
	var receipt struct {
		OK       bool     `json:"ok"`
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(out, &receipt); err != nil || !receipt.OK {
		t.Fatalf("invalid receipt: %s %v", out, err)
	}
	if !strings.Contains(string(out), "status_remote_locks_v1") {
		t.Fatalf("incomplete helper: %s", out)
	}
	out, err = exec.Command(exe, "native-runtime").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != helper {
		t.Fatalf("restart changed runtime: %v %s", err, out)
	}
	// A later start must detect corruption, not replace a potentially in-use
	// DLL and not silently select a foreign SVN client.
	dll := filepath.Join(filepath.Dir(helper), "libsvn_client-1.dll")
	if err := os.WriteFile(dll, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command(exe, "native-runtime").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "mismatch") {
		t.Fatalf("corruption not refused: %v %s", err, out)
	}
	got, _ = os.ReadFile(dll)
	if string(got) != "damaged" {
		t.Fatal("repaired immutable runtime in place")
	}
}
