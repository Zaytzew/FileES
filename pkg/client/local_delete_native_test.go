//go:build native_svn_probe && (windows || linux)

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
)

// Disposable real SVN repositories only. The client has no executable CLI;
// independent CLI is the fixture/oracle. Offline phases run with repo renamed.
func TestNativeLocalFileDeleteMetadata(t *testing.T) {
	for _, scenario := range []string{"present", "virtual", "recreated", "properties", "parent-properties",
		"directory", "wrong-base", "wrong-uuid", "scheduled-add", "scheduled-move", "excluded", "lock", "native-writer"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			repo, seed, wc := filepath.Join(root, "repo"), filepath.Join(root, "seed"), filepath.Join(root, "wc")
			run := func(tool string, args ...string) string {
				t.Helper()
				out, err := exec.Command(nativeFixtureTool(t, tool), args...).CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v %s", tool, args, err, out)
				}
				return string(out)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			run("svnadmin", "create", repo)
			must(os.MkdirAll(filepath.Join(seed, "dir"), 0700))
			must(os.WriteFile(filepath.Join(seed, "dir", "old.txt"), []byte("original history"), 0600))
			must(os.WriteFile(filepath.Join(seed, "sibling.txt"), []byte("sibling"), 0600))
			uri := svnurl.File(repo)
			run("svn", "import", "-q", "-m", "metadata deletion fixture", seed, uri)
			args := []string{"checkout", "-q", uri, wc}
			if scenario == "virtual" {
				args = append(args, "--depth", "empty")
			}
			run("svn", args...)
			must(os.Mkdir(filepath.Join(wc, ".filees"), 0700))
			if scenario == "virtual" {
				must(os.Mkdir(filepath.Join(wc, "dir"), 0700))
			}
			c := New(Options{NativeSVNPath: os.Getenv("FILEES_SVN_PROBE"), SvnPath: filepath.Join(root, "no-cli.exe")}).(*execClient)
			if c.nativeSVNPath == "" {
				t.Fatal("FILEES_SVN_PROBE required")
			}
			in := remoteDeleteFixture()
			in.RootURL, in.BaseRevision, in.Source = uri, 1, "dir/old.txt"
			in.UUID = strings.TrimSpace(run("svnlook", "uuid", repo))
			source := filepath.Join(wc, "dir", "old.txt")
			good := scenario == "present" || scenario == "virtual" || scenario == "recreated"
			switch scenario {
			case "properties":
				run("svn", "propset", "custom", "unsent", source)
			case "parent-properties":
				run("svn", "propset", "custom", "unsent", filepath.Dir(source))
			case "directory":
				in.Source = "dir"
			case "wrong-base":
				in.BaseRevision = 2
			case "wrong-uuid":
				in.UUID = "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
			case "scheduled-add":
				in.Source = "dir/new.txt"
				must(os.WriteFile(filepath.Join(wc, "dir", "new.txt"), []byte("unsent"), 0600))
				run("svn", "add", filepath.Join(wc, "dir", "new.txt"))
			case "scheduled-move":
				run("svn", "move", source, filepath.Join(wc, "dir", "moved.txt"))
			case "excluded":
				run("svn", "update", "--set-depth", "exclude", source)
			case "lock":
				run("svn", "lock", "-m", "fixture lock", source)
			case "native-writer":
				must(os.WriteFile(filepath.Join(wc, ".svn", "filees-native-writer-v1"), []byte("filees.native-writer/v1\n"+in.OperationID+"\n"), 0600))
			}
			state, err := c.LocalFileDeleteMetadata(t.Context(), wc, "inspect", in, 0)
			if !good {
				if err == nil {
					t.Fatalf("unsafe metadata preflight accepted: %s", state)
				}
				// Post-receipt phases must refuse the same mismatch too; merely
				// providing a revision never authorizes changing foreign metadata.
				for _, phase := range []string{"stage", "ack", "verify"} {
					if _, err := c.LocalFileDeleteMetadata(t.Context(), wc, phase, in, 3); err == nil {
						t.Fatal("unsafe metadata phase accepted", phase)
					}
				}
				if strings.TrimSpace(run("svnlook", "youngest", repo)) != "1" {
					t.Fatal("local phase committed")
				}
				return
			}
			must(err)
			want := "present"
			if scenario == "virtual" {
				want = "virtual"
			}
			if state != want {
				t.Fatal(state, want)
			}
			receipt, err := c.RemoteFileDelete(t.Context(), in)
			must(err)
			if receipt.Revision != 2 {
				t.Fatal(receipt)
			}
			if scenario != "virtual" {
				must(os.Remove(source))
				must(os.WriteFile(filepath.Join(wc, "sibling.txt"), []byte("unpublished sibling edit"), 0600))
			}
			if scenario == "recreated" {
				must(os.WriteFile(source, []byte("new unsent object"), 0600))
			}
			// Prove no remote request is needed by any metadata phase.
			offline := repo + "-offline"
			must(os.Rename(repo, offline))
			t.Cleanup(func() { _ = os.Rename(offline, repo) })
			if scenario != "virtual" {
				for _, phase := range []string{"ack", "verify"} {
					if _, err := c.LocalFileDeleteMetadata(t.Context(), wc, phase, in, receipt.Revision); err == nil {
						t.Fatal("unstaged node acknowledged", phase)
					}
				}
			}
			for _, phase := range []string{"stage", "stage", "ack", "ack", "stage", "verify", "verify"} {
				got, err := c.LocalFileDeleteMetadata(t.Context(), wc, phase, in, receipt.Revision)
				must(err)
				if got != "complete" && !(phase == "stage" && got == "scheduled") {
					t.Fatal(phase, got)
				}
			}
			if scenario == "recreated" {
				got, err := os.ReadFile(source)
				must(err)
				if string(got) != "new unsent object" {
					t.Fatal("recreated bytes changed")
				}
			} else if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatal("deleted path reappeared", err)
			}
			if scenario != "virtual" {
				out := run("svn", "status", wc)
				if !strings.Contains(out, "sibling.txt") || !strings.HasPrefix(out, "M") && !strings.Contains(out, "\nM") {
					t.Fatal("unselected edit acknowledged", out)
				}
				got, err := os.ReadFile(filepath.Join(wc, "sibling.txt"))
				must(err)
				if string(got) != "unpublished sibling edit" {
					t.Fatal("sibling bytes changed")
				}
			}
			must(os.Rename(offline, repo))
			if strings.TrimSpace(run("svnlook", "youngest", repo)) != "2" {
				t.Fatal("metadata ACK created revision")
			}
			if got := run("svn", "cat", "-r", "1", uri+"/dir/old.txt@1"); got != "original history" {
				t.Fatal("history changed", got)
			}
		})
	}
}
