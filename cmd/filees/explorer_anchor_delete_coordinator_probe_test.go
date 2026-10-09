//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

type anchorDeleteProbeClient struct {
	anchorMoveProbeClient
	deleteSends atomic.Int32
	interrupted atomic.Bool
	phase       string // set only while the isolated coordinator is idle
}

func (c *anchorDeleteProbeClient) RemoteFileDeletesEnabled() bool { return true }
func (c *anchorDeleteProbeClient) RemoteFileDelete(ctx context.Context, in client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
	c.deleteSends.Add(1)
	actual := in
	actual.RootURL = c.actual
	r, err := c.Client.(client.RemoteFileDeleter).RemoteFileDelete(ctx, actual)
	if r != nil {
		r.RemoteFileDelete = in
	}
	if err == nil && (c.phase == "reply" || c.phase == "recreated") {
		c.interrupted.Store(true)
		return nil, errors.New("isolated reply lost after commit")
	}
	return r, err
}
func (c *anchorDeleteProbeClient) InspectRemoteFileDelete(ctx context.Context, in client.RemoteFileDelete) (*client.RemoteFileDeleteReceipt, error) {
	actual := in
	actual.RootURL = c.actual
	r, err := c.Client.(client.RemoteFileDeleter).InspectRemoteFileDelete(ctx, actual)
	if r != nil {
		r.RemoteFileDelete = in
	}
	return r, err
}
func (c *anchorDeleteProbeClient) LocalFileDeleteMetadata(ctx context.Context, wc, phase string, in client.RemoteFileDelete, revision int64) (string, error) {
	in.RootURL = c.actual
	state, err := c.Client.(client.LocalFileDeleteReconciler).LocalFileDeleteMetadata(ctx, wc, phase, in, revision)
	if err == nil && phase == c.phase {
		c.interrupted.Store(true)
		return "", errors.New("isolated interruption after " + phase)
	}
	return state, err
}

// Product helper, real attachment/grant, real Service/Scanner, native SVN and
// real Cloud Filter callbacks. Only the network authority is substituted with
// a disposable file:// repository; no owner's point or running pair is touched.
func TestAnchorFileDeleteCoordinatorNativeEndToEnd(t *testing.T) {
	for _, scenario := range []string{"virtual", "metadata", "reply", "stage", "ack", "recreated", "read-only"} {
		t.Run(scenario, func(t *testing.T) {
			helper, native := probeTool(t, "FILEES_CFAPI"), probeTool(t, "FILEES_SVN_PROBE")
			cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
			base := t.TempDir()
			repo, seed, point := filepath.Join(base, "repo"), filepath.Join(base, "seed"), filepath.Join(base, "point")
			probeRun(t, base, admin, "create", repo)
			if err := os.Mkdir(seed, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"gone 新.txt", "stay.txt"} {
				if err := os.WriteFile(filepath.Join(seed, name), []byte("original bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, base, cli, "import", "-q", "-m", "delete fixture", seed, uri)
			depth := "empty"
			if scenario == "metadata" {
				depth = "infinity"
			}
			probeRun(t, base, cli, "checkout", "-q", "--depth", depth, uri, point)
			if depth == "infinity" {
				for _, name := range []string{"gone 新.txt", "stay.txt"} {
					if err := os.Remove(filepath.Join(point, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := os.MkdirAll(filepath.Join(point, ".filees", "state"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
					t.Errorf("unregister %s %v", out, err)
				}
			})
			svn := client.New(client.Options{SvnPath: filepath.Join(base, "absent-cli.exe"), NativeSVNPath: native, Timeout: time.Minute})
			authority := "svn+ssh://_filees-client@example/11111111-1111-4111-8111-111111111111"
			store, err := localrepo.Open(filepath.Join(base, "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.BeginAnchorAttach("server", "repo", point, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApproveAttach(record.OperationID, "server", "repo", authority, "rw"); err != nil {
				t.Fatal(err)
			}
			record, err = store.MarkAttached(record.OperationID, "repo")
			if err != nil {
				t.Fatal(err)
			}
			server := ipcserver.New("unused")
			state := server.RegisterRepoAccess("repo", authority, point, "server", "rw")
			reader := &anchorDeleteProbeClient{anchorMoveProbeClient: anchorMoveProbeClient{anchorBatchProbeReader: anchorBatchProbeReader{Client: svn, actual: uri, authority: authority}}, phase: scenario}
			service := &commit.Service{Cli: svn}
			service.BindWorkingCopy("repo", point)
			state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
				return service.WithLocalStatus(ctx, point, paths, confirm)
			})
			state.SetLocalSubtreeStatusFunc(func(ctx context.Context, path string, confirm func([]client.StatusEntry) error) error {
				return service.WithLocalSubtreeStatus(ctx, point, path, confirm)
			})
			state.SetLocalMetadataGuardFunc(func(ctx context.Context, op func() error) error {
				return service.WithLocalMetadataGuard(ctx, point, op)
			})
			state.SetFileDeleteFunc(func(ctx context.Context, source string, p *watcher.FileDeleteSnapshot, op func(*watcher.FileDeleteSnapshot) error, finish func() error) error {
				return service.WithRemoteFileDelete(ctx, point, source, p, op, finish)
			})
			m := &anchorManager{helper: helper, profiles: filepath.Join(base, "profiles.json"), lifecycle: store, repos: server.RepoState, newSVN: func(string) (client.Client, error) { return reader, nil }, log: talk.With("delete-native"), stateEvery: time.Hour, options: &anchorOptions{}}
			m.options.servers.Store(map[string]bool{"server": true})
			if scenario == "metadata" {
				// The full-WC prerequisite already has canonical FileES remote
				// identities and a bound tree. Cold legacy seeding is a different
				// migration and may legitimately require content for SVN status.
				gone, _ := anchorRemoteIdentity("gone 新.txt", 1, 14)
				stay, _ := anchorRemoteIdentity("stay.txt", 1, 14)
				p := newLocalStateProbe(t, point, "", fmt.Sprintf("f\t14\t%s\tgone 新.txt\nf\t14\t%s\tstay.txt\n", gone, stay))
				p.stop()
				guards, root, err := guardAnchorMoveState(record)
				if err != nil {
					t.Fatal(err)
				}
				closeAnchorReleaseGuards(guards)
				tree := anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: record.OperationID, URL: authority, Root: root, Revision: 1,
					Nodes: []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: "gone 新.txt", Kind: "file", Size: 14}, Identity: gone}, {HistoryTreeNode: client.HistoryTreeNode{Path: "stay.txt", Kind: "file", Size: 14}, Identity: stay}}}
				if err := saveAnchorRemoteTree(record, tree); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(anchorSeed{Schema: anchorSeedSchema, Revision: 1, Files: 2})
				if err := os.WriteFile(anchorSeedPath(point), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			m.running = map[string]anchorConnection{anchorKey(record): {ctx: ctx, cancel: cancel, done: done}}
			go func() { m.keep(ctx, record); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			waitFor(t, 30*time.Second, "provider", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running[anchorKey(record)].bridge != nil })
			waitFor(t, 30*time.Second, "remote tree", func() bool { _, err := os.Stat(anchorTreePath(point)); return err == nil })
			manifest := filepath.Join(point, ".filees", "state", "manifest.json")
			bindScanner := func() *watcher.Scanner {
				scanner, err := watcher.NewScanner(watcher.Options{WC: point, StatePath: manifest, PublicationPending: func() bool { return commit.HasPendingLocalMutation(point) }})
				if err != nil {
					t.Fatal(err)
				}
				scanCtx, stop := context.WithCancel(t.Context())
				events := scanner.Start(scanCtx)
				waitFor(t, 5*time.Second, "scanner ready", func() bool {
					p, err := scanner.CapturePublication([]string{"gone 新.txt"})
					return err == nil && p.Sequence > 0
				})
				stop()
				for range events {
				}
				service.FenceFileDelete, service.RecoverFileDelete = scanner.WithFileDeletePreparation, scanner.WithFileDeleteRecovery
				return scanner
			}
			bindScanner()
			if scenario == "read-only" {
				server.RegisterRepoAccess("repo", authority, point, "server", "r")
			}
			beforeFetches := reader.fetches.Load()
			err = os.Remove(filepath.Join(point, "gone 新.txt"))
			if scenario == "read-only" {
				if err == nil || commit.HasPendingRemoteFileDelete(point) || reader.deleteSends.Load() != 0 {
					t.Fatal("read-only granted delete", err)
				}
				return
			}
			if err != nil {
				t.Fatal("OS delete", err)
			}
			waitFor(t, 15*time.Second, "one remote send", func() bool { return reader.deleteSends.Load() == 1 })
			if scenario == "reply" || scenario == "stage" || scenario == "ack" || scenario == "recreated" {
				waitFor(t, 15*time.Second, "injected completed phase", func() bool { return reader.interrupted.Load() })
				// Wait until the typed completion worker exits before injecting a
				// fresh scanner and exercising startup recovery with old evidence.
				cancel()
				<-done
				if !commit.HasPendingRemoteFileDelete(point) {
					t.Fatal("interruption lost fence")
				}
				if scenario == "recreated" {
					if err := os.WriteFile(filepath.Join(point, "gone 新.txt"), []byte("new unsent bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				reader.phase = ""
				bindScanner()
				ctx, cancel = context.WithCancel(t.Context())
				done = make(chan struct{})
				m.running = map[string]anchorConnection{anchorKey(record): {ctx: ctx, cancel: cancel, done: done}}
				go func() { m.keep(ctx, record); close(done) }()
			}
			waitFor(t, 30*time.Second, "delete retired", func() bool { return !commit.HasPendingRemoteFileDelete(point) })
			if reader.deleteSends.Load() != 1 || reader.fetches.Load() != beforeFetches {
				t.Fatal("replayed or hydrated", reader.deleteSends.Load(), reader.fetches.Load(), beforeFetches)
			}
			guards, root, err := guardAnchorMoveState(record)
			if err != nil {
				t.Fatal(err)
			}
			closeAnchorReleaseGuards(guards)
			tree, err := loadAnchorRemoteTree(record, root)
			if err != nil || tree.Revision != 2 || len(tree.Nodes) != 1 || tree.Nodes[0].Path != "stay.txt" {
				t.Fatal("manifest did not scoped ACK", tree, err)
			}
			if scenario == "recreated" {
				raw, err := os.ReadFile(filepath.Join(point, "gone 新.txt"))
				if err != nil || string(raw) != "new unsent bytes" {
					t.Fatal("recreation lost", err)
				}
			}
			history, err := exec.Command(cli, "cat", "-r", "1", uri+"/gone%20%E6%96%B0.txt@1").Output()
			if err != nil || string(history) != "original bytes" {
				t.Fatal("history lost", err)
			}
			if scenario == "virtual" {
				// The second file retains its older pinned identity. Current
				// remote BASE and local metadata BASE must not be conflated.
				waitFor(t, 5*time.Second, "bridge rearmed", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running[anchorKey(record)].bridge != nil })
				if err := os.Remove(filepath.Join(point, "stay.txt")); err != nil {
					t.Fatal("second delete", err)
				}
				waitFor(t, 30*time.Second, "second deletion retired", func() bool { return reader.deleteSends.Load() == 2 && !commit.HasPendingRemoteFileDelete(point) })
				if reader.fetches.Load() != beforeFetches {
					t.Fatal("second deletion hydrated")
				}
			}
		})
	}
}
