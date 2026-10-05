//go:build windows && native_cfapi_probe && !nocfapi

package commit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/watcher"
)

// Real CFAPI metadata plus the production publication service: a retained
// local file is user work, not an unread seed to drop silently from the queue.
func TestNativeCFAPILocalFilePublishesAfterRename(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(fmt.Sprintf("identified=%v", identified), func(t *testing.T) {
			helper, native, cli, admin := os.Getenv("FILEES_CFAPI"), os.Getenv("FILEES_SVN_PROBE"), os.Getenv("FILEES_PROBE_SVN"), os.Getenv("FILEES_PROBE_SVNADMIN")
			if helper == "" || native == "" || cli == "" || admin == "" {
				t.Fatal("explicit CFAPI, native SVN, CLI and svnadmin fixture paths required")
			}
			run := func(args ...string) []byte {
				t.Helper()
				out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
				if err != nil {
					t.Fatalf("fixture: %v %s", err, out)
				}
				return out
			}
			root := t.TempDir()
			repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "point")
			run(admin, "create", repo)
			uri := svnurl.File(repo)
			run(cli, "checkout", "-q", uri, wc)
			for _, dir := range []string{"state", "commit_cache"} {
				if err := os.MkdirAll(filepath.Join(wc, ".filees", dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			old, next := filepath.Join(wc, "old.txt"), filepath.Join(wc, "new.txt")
			if err := os.WriteFile(old, []byte("retained user bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			run(cli, "add", "-q", old)
			run(cli, "commit", "-q", "-m", "fixture", wc)
			run(helper, "register", "--root", wc, "--identity", "synthetic-commit-state")
			t.Cleanup(func() { run(helper, "unregister", "--root", wc) })
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, helper, "connect", "--root", wc)
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = input.Close(); _ = command.Wait() })
			reader := bufio.NewScanner(output)
			if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
				t.Fatal("provider not connected")
			}
			var state struct {
				USN string `json:"usn"`
			}
			if err := json.Unmarshal(run(helper, "file-state", "--root", wc, "--rel", "old.txt"), &state); err != nil || state.USN == "" {
				t.Fatalf("state: %v", err)
			}
			if _, err := io.WriteString(input, "state-set\tprobe\told.txt\t"+state.USN+"\t1\n"); err != nil {
				t.Fatal(err)
			}
			if !reader.Scan() || reader.Text() != "state-set\tprobe\t0x00000000" {
				t.Fatalf("state acknowledgement: %s", reader.Text())
			}
			if err := os.Rename(old, next); err != nil {
				t.Fatal(err)
			}
			if local, _ := cloudfiles.LocalState(next); !local {
				t.Fatal("renamed file lost local identity")
			}
			s := &Service{
				Cli:     client.New(client.Options{NativeSVNPath: native, SvnPath: filepath.Join(root, "missing-cli.exe"), Timeout: 10 * time.Second}),
				RepoURL: uri, wc: wc, repoID: "fixture", UUID: "fixture",
				Rules:   Rules{MaxBatchFiles: 100, MaxBatchBytes: 1024 * 1024},
				staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees/commit_cache/cache.json"),
			}
			event := watcher.Event{Path: next, Rel: "new.txt", Type: watcher.EntryFile, Op: watcher.Added}
			if identified {
				event.Op, event.OldRel, event.IdentityVerified = watcher.Renamed, "old.txt", true
			}
			s.addEvent(event)
			if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
				t.Fatal(err)
			}
			if len(s.staging) != 0 {
				t.Fatal("local work remained queued")
			}
			if got := string(run(cli, "cat", uri+"/new.txt")); got != "retained user bytes" {
				t.Fatalf("published: %q", got)
			}
			listing := string(run(cli, "list", uri))
			if identified && strings.Contains(listing, "old.txt") {
				t.Fatal("identified rename left original server path")
			}
		})
	}
}
