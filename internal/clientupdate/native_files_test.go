package clientupdate

import (
	"encoding/json"
	"filees/internal/nativeruntime"
	"filees/internal/releaseenvelope"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func addRuntimeFixture(t *testing.T, bundle, text string) []byte {
	t.Helper()
	contents := map[string]string{nativeruntime.Executable: text, "test.dll": "library", "notices/DEPENDENCIES.json": "fixture"}
	for _, name := range []string{"FileES-LICENSE.txt", "Subversion-LICENSE.txt", "Subversion-NOTICE.txt", "MSVC-Redist.txt", "RUNTIME-NOTICE.txt", "apr-copyright.txt", "apr-util-copyright.txt", "expat-copyright.txt", "openssl-copyright.txt", "serf-copyright.txt", "sqlite3-copyright.txt", "zlib-copyright.txt"} {
		contents["notices/"+name] = "license"
	}
	m := nativeruntime.FileManifest{Schema: "filees.native-files/v1", Platform: "windows-amd64"}
	for n, c := range contents {
		m.Files = append(m.Files, nativeruntime.FileDigest{Name: n, SHA256: nativeruntime.ID([]byte(c)), Size: int64(len(c))})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	raw, _ := json.Marshal(m)
	base := nativeruntime.FilesRoot(filepath.Join(bundle, "bin"), raw)
	for n, c := range contents {
		p := filepath.Join(base, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "bin", "native-svn.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestSidecarInstallerKeepsPreviousVersionAndRefusesCorruptionBeforeBinarySwap(t *testing.T) {
	installer := newInstaller(t.TempDir(), "")
	old := writeBundle(t, completeBundle("old"))
	oldRaw := addRuntimeFixture(t, old, "old helper")
	if err := installer.applyStaged(old); err != nil {
		t.Fatal(err)
	}
	next := writeBundle(t, completeBundle("next"))
	nextRaw := addRuntimeFixture(t, next, "new helper")
	if err := installer.applyStaged(next); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{oldRaw, nextRaw} {
		if _, err := nativeruntime.VerifyFiles(nativeruntime.FilesRoot(installer.Paths.InstallDir, raw), raw); err != nil {
			t.Fatal(err)
		}
	}
	// Locally selected rollback reuses the old immutable runtime, not whatever
	// files happened to be newest on disk. Signed antirollback policy is separate.
	if err := installer.applyStaged(old); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(nativeruntime.FilesRoot(filepath.Join(next, "bin"), nextRaw), "test.dll")
	if err := os.WriteFile(bad, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installer.applyStaged(next); err == nil {
		t.Fatal("accepted broken runtime")
	}
	data, _ := os.ReadFile(filepath.Join(installer.Paths.InstallDir, "filees.exe"))
	if string(data) != "daemon old" {
		t.Fatal("swapped binary before verifying all sidecars")
	}
}
func TestNativeFilesBundleIsDistinguishableAndCannotHideDuplicateBundle(t *testing.T) {
	art, err := selectBundle([]releaseenvelope.Artifact{{Kind: "installer", Source: "file.msi"}, {Kind: "bundle-native-files", Source: "client.tar.gz"}})
	if err != nil || art.Kind != "bundle-native-files" {
		t.Fatal(art, err)
	}
	if _, err := selectBundle([]releaseenvelope.Artifact{{Kind: "bundle", Source: "old.tar.gz"}, {Kind: "bundle-native-files", Source: "new.tar.gz"}}); err == nil {
		t.Fatal("accepted ambiguous bundle")
	}
}
