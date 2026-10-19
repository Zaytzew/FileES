//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/localrepo"
)

type conversionBoundProbeReader struct {
	anchorBatchProbeReader
	headReads int
}

func (r *conversionBoundProbeReader) Revision(context.Context, string) (int64, error) {
	r.headReads++
	return 0, errors.New("conversion must not move to a later HEAD")
}

// A real isolated registration and placeholder tree, deliberately older than
// server HEAD. This covers the bound seed, not the complete conversion adapter.
func TestConversionBoundSeedNativeCFAPIWithoutHydration(t *testing.T) {
	helper, native := probeTool(t, "FILEES_CFAPI"), probeTool(t, "FILEES_SVN_PROBE")
	cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
	base := t.TempDir()
	repo, source := filepath.Join(base, "repo"), filepath.Join(base, "source")
	probeRun(t, base, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "remote.txt"), []byte("bound revision one"), 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, base, cli, "import", "-q", "-m", "r1", source, uri)
	probeRun(t, base, cli, "mkdir", "-q", "-m", "r2", uri+"/later-only")
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	reader := &conversionBoundProbeReader{anchorBatchProbeReader: anchorBatchProbeReader{Client: svn, actual: uri, authority: "svn+ssh://fixture/repo"}}
	for _, rev := range []int64{0, 1} {
		t.Run(map[int64]string{0: "empty-r0", 1: "older-r1"}[rev], func(t *testing.T) {
			point := filepath.Join(t.TempDir(), "point")
			probeRun(t, base, cli, "checkout", "-q", "--depth", "empty", "-r", map[int64]string{0: "0", 1: "1"}[rev], uri, point)
			if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			m := &anchorManager{helper: helper, newSVN: func(string) (client.Client, error) { return reader, nil }}
			answer, err := m.call(t.Context(), "", "register", "--root", point, "--identity", "fixture\x1frepo")
			if err != nil || !answer.OK {
				t.Fatal("fixture registration", answer, err)
			}
			t.Cleanup(func() {
				if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
					t.Errorf("fixture unregister: %s %v", out, err)
				}
			})
			r := localrepo.Record{ServerID: "fixture", RepoID: "repo", RepoURL: reader.authority, LocalPath: point}
			if err := m.seedAt(t.Context(), r, rev); err != nil {
				t.Fatal(err)
			}
			if reader.headReads != 0 {
				t.Fatal("conversion used HEAD")
			}
			raw, err := os.ReadFile(anchorSeedPath(point))
			if err != nil {
				t.Fatal(err)
			}
			var seed anchorSeed
			if json.Unmarshal(raw, &seed) != nil || seed.Revision != rev {
				t.Fatal("bound receipt", string(raw))
			}
			if _, err := os.Lstat(filepath.Join(point, "later-only")); !os.IsNotExist(err) {
				t.Fatal("tree expanded to HEAD", err)
			}
			if rev == 0 {
				if _, err := os.Lstat(filepath.Join(point, "remote.txt")); !os.IsNotExist(err) {
					t.Fatal("r0 is not empty", err)
				}
				return
			}
			file := filepath.Join(point, "remote.txt")
			info, err := os.Stat(file)
			if err != nil || !cloudfiles.IsPlaceholder(file) || !cloudfiles.RecallsOnRead(info) {
				t.Fatal("file hydrated or not a placeholder", err)
			}
			state, err := m.call(t.Context(), "", "file-state", "--root", point, "--rel", "remote.txt")
			if err != nil || !state.OK || state.Local || state.OnDisk != 0 {
				t.Fatal("unexpected local content", state, err)
			}
		})
	}
}
