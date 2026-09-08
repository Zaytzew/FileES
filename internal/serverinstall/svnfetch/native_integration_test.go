//go:build native_svn_probe && windows

package svnfetch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

func TestNativeCatReleaseBytesNoCLIFallback(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, seed := filepath.Join(root, "repo"), filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "manifest.json"), []byte("{\"exact\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"svnadmin", "create", repo}, {"svn", "import", seed, svnurl.File(repo), "-m", "fixture"}} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	fetch := SVN{NativeProgram: helper, Program: filepath.Join(root, "absent-cli.exe"), RepoURL: svnurl.File(repo)}
	got, err := fetch.Cat(t.Context(), "manifest.json")
	if err != nil || string(got) != "{\"exact\":true}\n" {
		t.Fatalf("%q %v", got, err)
	}
	fetch.NativeProgram = filepath.Join(root, "absent-helper.exe")
	if _, err := fetch.Cat(t.Context(), "manifest.json"); err == nil {
		t.Fatal("missing helper accepted")
	}
}
