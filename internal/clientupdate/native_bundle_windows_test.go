//go:build windows && native_svn_package

package clientupdate

import (
	"archive/tar"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in acceptance of the actual bundle, using the unchanged four-file
// installer, not a separate payload-copy script. No installed user processes
// or configuration are touched. Signature tests live in releaseenvelope.
func TestNativeBundleThroughFourFileInstaller(t *testing.T) {
	bundle := os.Getenv("FILEES_TEST_NATIVE_BUNDLE")
	if !filepath.IsAbs(bundle) {
		t.Fatal("absolute FILEES_TEST_NATIVE_BUNDLE required")
	}
	var entries []tarEntry
	for _, name := range RequiredBundleFiles() {
		data, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarEntry{name: name, typeflag: tar.TypeReg, mode: 0755, data: string(data)})
	}
	archive := makeBundle(t, entries...)
	resolved := stagedRelease(archive)
	root := t.TempDir()
	installer := newInstaller(filepath.Join(root, "installed"), filepath.Join(root, "config.json"))
	installer.Stager = BundleStager{Fetcher: artifactFetcher{"releases/r1/desktop/linux-amd64/client.tar.gz": archive}, Root: root}
	if err := os.WriteFile(installer.Paths.ConfigPath, []byte("do not change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installer.Apply(t.Context(), resolved); err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadDir(installer.Paths.InstallDir)
	if err != nil || len(installed) != 4 {
		t.Fatalf("installer layout changed: %v %v", installed, err)
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
	rel, err := filepath.Rel(cacheRoot, helper)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.Base(helper) != "filees-svn.exe" {
		t.Fatalf("not an embedded helper: %q %v", helper, err)
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
