//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// Exercise the actual Cloud Filter provider with two synthetic SVN files.
// The point returns to attached before reconcile can reap its old connection.
func TestAnchorRelocationReconnectsAndReadsRemainingPlaceholder(t *testing.T) {
	helper, native := probeTool(t, "FILEES_CFAPI"), probeTool(t, "FILEES_SVN_PROBE")
	cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "app-data"))
	repo, source := filepath.Join(root, "repo"), filepath.Join(root, "source")
	oldPoint, point := filepath.Join(root, "Punkt-przed"), filepath.Join(root, "Punkt-przed")
	newPoint := filepath.Join(root, "Przeniesiony-Zażółć-東京")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	retained, remote := []byte("already downloaded bytes"), []byte("download after relocation")
	for name, data := range map[string][]byte{"local.txt": retained, "remote.txt": remote} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "relocation fixture", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAnchorAttach("server", "repo", point, false)
	if err != nil {
		t.Fatal(err)
	}
	const authority = "svn+ssh://_filees-client@example/11111111-1111-4111-8111-111111111111"
	if _, err := store.ApproveAttach(record.OperationID, "server", "repo", authority, "rw"); err != nil {
		t.Fatal(err)
	}
	record, err = store.MarkAttached(record.OperationID, "repo")
	if err != nil {
		t.Fatal(err)
	}
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	server := ipcserver.New("unused")
	service := &commit.Service{Cli: svn}
	bind := func() *ipcserver.RepoState {
		state := server.RegisterRepoAccess("repo", authority, point, "server", "rw")
		service.BindWorkingCopy("repo", point)
		state.SetAdoptFunc(func(ctx context.Context, rel string, revision int64) error {
			_, err := svn.(interface {
				UpdateAdoptPath(context.Context, string, string, int64) (string, error)
			}).UpdateAdoptPath(ctx, point, rel, revision)
			return err
		})
		state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
			return service.WithLocalStatus(ctx, point, paths, confirm)
		})
		return state
	}
	state := bind()
	m := &anchorManager{helper: helper, nativeSVN: native, lifecycle: store, stateEvery: 50 * time.Millisecond, options: &anchorOptions{},
		repos: func(string, string) *ipcserver.RepoState { return state },
		newSVN: func(string) (client.Client, error) {
			return anchorBatchProbeReader{Client: svn, actual: uri, authority: authority}, nil
		},
	}
	m.options.servers.Store(map[string]bool{"server": true})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		m.mu.Lock()
		var done []chan struct{}
		for _, connection := range m.running {
			connection.cancel()
			done = append(done, connection.done)
		}
		m.mu.Unlock()
		for _, finished := range done {
			select {
			case <-finished:
			case <-time.After(15 * time.Second):
				t.Error("provider did not drain")
			}
		}
		for _, path := range []string{newPoint, oldPoint} {
			if out, err := exec.Command(helper, "unregister", "--root", path).CombinedOutput(); err != nil {
				t.Errorf("synthetic point cleanup: %s %v", out, err)
			}
		}
	})
	connected := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		connection := m.running[anchorKey(record)]
		return connection.root == point && connection.bridge != nil && connection.providerCtx != nil && connection.providerCtx.Err() == nil
	}
	m.reconcile(ctx)
	waitFor(t, 30*time.Second, "initial provider", connected)
	seed, err := os.ReadFile(anchorSeedPath(point))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(point, "local.txt"))
	if err != nil || !bytes.Equal(got, retained) {
		t.Fatalf("initial hydration: %q %v", got, err)
	}
	waitFor(t, 15*time.Second, "retained local file", func() bool {
		local, clean := cloudfiles.LocalState(filepath.Join(point, "local.txt"))
		return local && clean
	})
	if !cloudfiles.NotOnDisk(filepath.Join(point, "remote.txt")) {
		t.Fatal("unopened file was hydrated")
	}
	m.mu.Lock()
	old := m.running[anchorKey(record)]
	m.mu.Unlock()
	if _, err := store.BeginRelocation("server", "repo", newPoint, true); err != nil {
		t.Fatal(err)
	}
	m.reconcile(ctx)
	select {
	case <-old.done:
	case <-time.After(15 * time.Second):
		t.Fatal("old provider did not stop")
	}
	if err := os.Rename(point, newPoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRelocation(record.OperationID); err != nil {
		t.Fatal(err)
	}
	// No reconcile while relocating after the old worker finished. This is
	// the previously failing ordering, not a restart of the daemon.
	point = newPoint
	state = bind()
	m.reconcile(ctx)
	waitFor(t, 30*time.Second, "provider after rename", connected)
	answer, err := m.call(ctx, "", "info", "--root", point)
	if err != nil || !answer.OK || answer.Status != 1 {
		t.Fatalf("provider status after rename: %+v %v", answer, err)
	}
	if after, err := os.ReadFile(anchorSeedPath(point)); err != nil || !bytes.Equal(seed, after) {
		t.Fatal("move changed the seed")
	}
	if !cloudfiles.NotOnDisk(filepath.Join(point, "remote.txt")) {
		t.Fatal("reconnect eagerly downloaded remote file")
	}
	got, err = os.ReadFile(filepath.Join(point, "remote.txt"))
	if err != nil || !bytes.Equal(got, remote) {
		t.Fatalf("hydration after rename: %q %v", got, err)
	}
	waitFor(t, 15*time.Second, "adoption after rename", func() bool {
		local, clean := cloudfiles.LocalState(filepath.Join(point, "remote.txt"))
		return local && clean
	})
	if got, err := os.ReadFile(filepath.Join(point, "local.txt")); err != nil || !bytes.Equal(got, retained) {
		t.Fatal("move lost local bytes")
	}
	t.Log("real CFAPI reconnect, Unicode rename, retained bytes, unchanged seed and lazy download PASS; synthetic local SVN only")
}
