//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

type anchorMoveProbeClient struct {
	anchorBatchProbeReader
	fetches, sends atomic.Int32
	interrupt      string
}

func (c *anchorMoveProbeClient) HistoryFetchFile(ctx context.Context, target string, revision int64, out string) (int64, error) {
	c.fetches.Add(1)
	return c.anchorBatchProbeReader.HistoryFetchFile(ctx, target, revision, out)
}
func (c *anchorMoveProbeClient) RemoteTreeMovesEnabled() bool { return true }
func (c *anchorMoveProbeClient) HistoryRepositoryUUID(ctx context.Context, target string) (string, error) {
	return c.Client.(interface {
		HistoryRepositoryUUID(context.Context, string) (string, error)
	}).HistoryRepositoryUUID(ctx, c.target(target))
}
func (c *anchorMoveProbeClient) RemoteTreeMove(ctx context.Context, move client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
	c.sends.Add(1)
	original := move
	move.RootURL = c.actual
	got, err := c.Client.(client.RemoteTreeMover).RemoteTreeMove(ctx, move)
	if got != nil {
		got.RemoteTreeMove = original
	}
	if err == nil && c.interrupt == "reply" {
		c.interrupt = ""
		return nil, errors.New("isolated lost reply")
	}
	return got, err
}
func (c *anchorMoveProbeClient) InspectRemoteTreeMove(ctx context.Context, move client.RemoteTreeMove) (*client.RemoteTreeReceipt, error) {
	original := move
	move.RootURL = c.actual
	got, err := c.Client.(client.RemoteTreeMover).InspectRemoteTreeMove(ctx, move)
	if got != nil {
		got.RemoteTreeMove = original
	}
	return got, err
}
func (c *anchorMoveProbeClient) LocalTreeMetadata(ctx context.Context, wc, phase string, move client.RemoteTreeMove, rev int64) (string, error) {
	move.RootURL = c.actual
	got, err := c.Client.(client.LocalTreeReconciler).LocalTreeMetadata(ctx, wc, phase, move, rev)
	if err == nil && c.interrupt == phase {
		c.interrupt = ""
		return "", errors.New("isolated interruption after " + phase)
	}
	return got, err
}

func TestAnchorTreeMoveNativeEndToEnd(t *testing.T) {
	for _, scenario := range []string{"", "remote", "reply", "stage", "ack", "restart", "nested", "nested-remote"} {
		t.Run("phase-"+scenario, func(t *testing.T) {
			interrupt := scenario
			if scenario == "nested" {
				interrupt = ""
			}
			if scenario == "nested-remote" {
				interrupt = "remote"
			}
			helper, native := probeTool(t, "FILEES_CFAPI"), probeTool(t, "FILEES_SVN_PROBE")
			cli, admin := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN")
			root := t.TempDir()
			repo, seed, point := filepath.Join(root, "repo"), filepath.Join(root, "seed"), filepath.Join(root, "point")
			src, dst := "źródło", "cel"
			if strings.HasPrefix(scenario, "nested") {
				dst = "01_PROJEKT/nowa nazwa"
				if err := os.MkdirAll(filepath.Join(seed, "01_PROJEKT"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			probeRun(t, root, admin, "create", repo)
			if err := os.MkdirAll(filepath.Join(seed, src), 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"local.txt", "cloud.txt"} {
				if err := os.WriteFile(filepath.Join(seed, src, name), []byte("original bytes\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, root, cli, "import", "-q", "-m", "move seed", seed, uri)
			probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
			if err := os.MkdirAll(filepath.Join(point, ".filees", "state"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
					t.Errorf("unregister %v %s", err, out)
				}
			})
			svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
			authority := "svn+ssh://_filees-client@example/11111111-1111-4111-8111-111111111111"
			store, err := localrepo.Open(filepath.Join(root, "lifecycle.json"))
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
			reader := &anchorMoveProbeClient{anchorBatchProbeReader: anchorBatchProbeReader{Client: svn, actual: uri, authority: authority}}
			journal, err := activity.Open(filepath.Join(root, "activity.json"), 20)
			if err != nil {
				t.Fatal(err)
			}
			service := &commit.Service{Cli: svn}
			service.BindWorkingCopy("repo", point)
			state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
				return service.WithLocalStatus(ctx, point, paths, confirm)
			})
			state.SetAdoptFunc(func(ctx context.Context, rel string, revision int64) error {
				_, err := svn.(interface {
					UpdateAdoptPath(context.Context, string, string, int64) (string, error)
				}).UpdateAdoptPath(ctx, point, rel, revision)
				return err
			})
			state.SetTreeMutationFunc(func(ctx context.Context, src, dst string, p *watcher.TreeMoveSnapshot, op func(*watcher.TreeMoveSnapshot) error, finish func() error) error {
				return service.WithRemoteTreeMove(ctx, point, src, dst, p, op, finish)
			})
			m := &anchorManager{activity: journal, helper: helper, profiles: filepath.Join(root, "profiles.json"), lifecycle: store, repos: server.RepoState, newSVN: func(string) (client.Client, error) { return reader, nil }, log: talk.With("move-native"), stateEvery: time.Hour, options: &anchorOptions{}}
			m.options.servers.Store(map[string]bool{"server": true})
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			m.running = map[string]anchorConnection{anchorKey(record): {ctx: ctx, cancel: cancel, done: done}}
			go func() { m.keep(ctx, record); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			waitFor(t, 30*time.Second, "move provider", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running[anchorKey(record)].bridge != nil })
			waitFor(t, 30*time.Second, "remote tree", func() bool { _, err := os.Stat(anchorTreePath(point)); return err == nil })
			if interrupt != "remote" {
				if err := m.downloadFile(t.Context(), record, src+"/local.txt"); err != nil {
					t.Fatal("adopt local", err)
				}
				m.mu.Lock()
				activeBridge := m.running[anchorKey(record)].bridge
				m.mu.Unlock()
				cursor := ""
				if err := m.refreshLocalStates(t.Context(), record, activeBridge, &cursor); err != nil {
					t.Fatal("local identity", err)
				}
			}
			manifest := filepath.Join(point, ".filees", "state", "manifest.json")
			scanner, err := watcher.NewScanner(watcher.Options{WC: point, StatePath: manifest, UseMD5: false, PublicationPending: func() bool { return commit.HasPendingLocalMutation(point) }})
			if err != nil {
				t.Fatal(err)
			}
			scanCtx, stop := context.WithCancel(t.Context())
			events := scanner.Start(scanCtx)
			waitFor(t, 5*time.Second, "scanner baseline", func() bool { p, err := scanner.CapturePublication([]string{src}); return err == nil && p.Sequence > 0 })
			stop()
			for range events {
			}
			service.FenceTreeMove = scanner.WithTreeMove
			service.RecoverTreeMove = scanner.WithTreeMoveRecovery
			uuidRepo, err := svn.(interface {
				HistoryRepositoryUUID(context.Context, string) (string, error)
			}).HistoryRepositoryUUID(t.Context(), uri)
			if err != nil {
				t.Fatal(err)
			}
			move := client.RemoteTreeMove{RootURL: authority, UUID: uuidRepo, BaseRevision: 1, OperationID: uuid.NewString(), Source: src, Destination: dst, Message: "isolated tree move"}
			oldFetch := reader.fetches.Load()
			if interrupt != "remote" {
				reader.interrupt = interrupt
			}
			if interrupt == "restart" {
				reader.interrupt = "ack"
			}
			if interrupt == "" || interrupt == "remote" {
				adapter := anchorTreeMoveAdapter{m}
				selection := contract.RepoAnchorMoveSelection{ServerID: "server", RepoID: "repo", Source: src, Destination: dst}
				preview, e := adapter.MovePlan(t.Context(), selection)
				if e != nil || reader.sends.Load() != 0 || commit.HasPendingRemoteTree(point) {
					t.Fatal("preview had an effect", e)
				}
				if e := adapter.MoveExecute(t.Context(), contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: selection, Fingerprint: strings.Repeat("ab", 32)}); e == nil || reader.sends.Load() != 0 {
					t.Fatal("stale consent reached remote")
				}
				err = adapter.MoveExecute(t.Context(), contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: selection, Fingerprint: preview.Fingerprint})
			} else {
				err = m.moveTree(t.Context(), record, move)
			}
			if (interrupt == "" || interrupt == "remote") && err != nil {
				t.Fatal("move", err)
			}
			if interrupt != "" && interrupt != "remote" {
				if err == nil || !commit.HasPendingRemoteTree(point) {
					t.Fatal("interruption lost fence", err)
				}
				location := src
				if _, err := os.Stat(filepath.Join(point, dst)); err == nil {
					location = dst
				}
				if err := os.WriteFile(filepath.Join(point, location, "local.txt"), []byte("later unsent edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
				fresh, err := watcher.NewScanner(watcher.Options{WC: point, StatePath: manifest, PublicationPending: func() bool { return commit.HasPendingLocalMutation(point) }})
				if err != nil {
					t.Fatal(err)
				}
				service.RecoverTreeMove = fresh.WithTreeMoveRecovery
				if interrupt == "restart" {
					cancel()
					<-done
					// Actual startup can connect Windows before its RW writer/scanner
					// callback is installed. Only that local readiness may be retried.
					state.SetTreeMutationFunc(nil)
					timer := time.AfterFunc(500*time.Millisecond, func() {
						state.SetTreeMutationFunc(func(ctx context.Context, src, dst string, p *watcher.TreeMoveSnapshot, op func(*watcher.TreeMoveSnapshot) error, finish func() error) error {
							return service.WithRemoteTreeMove(ctx, point, src, dst, p, op, finish)
						})
					})
					t.Cleanup(func() { timer.Stop() })
					m = &anchorManager{activity: journal, helper: helper, profiles: filepath.Join(root, "profiles.json"), lifecycle: store, repos: server.RepoState, newSVN: func(string) (client.Client, error) { return reader, nil }, log: talk.With("fresh-move-native"), stateEvery: time.Hour, options: m.options}
					ctx, cancel = context.WithCancel(t.Context())
					done = make(chan struct{})
					m.running = map[string]anchorConnection{anchorKey(record): {ctx: ctx, cancel: cancel, done: done}}
					go func() { m.keep(ctx, record); close(done) }()
					waitFor(t, 30*time.Second, "fresh provider", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.running[anchorKey(record)].bridge != nil })
					waitFor(t, 30*time.Second, "automatic fresh-provider recovery", func() bool { return !commit.HasPendingRemoteTree(point) })
				}
				m.mu.Lock()
				bridge := m.running[anchorKey(record)].bridge
				m.mu.Unlock()
				if commit.HasPendingRemoteTree(point) {
					if err := m.recoverTreeMove(t.Context(), record, bridge); err != nil {
						t.Fatal("recover", err)
					}
				}
			}
			if reader.sends.Load() != 1 || commit.HasPendingRemoteTree(point) {
				t.Fatal("replayed or still fenced", reader.sends.Load())
			}
			if reader.fetches.Load() != oldFetch {
				t.Fatal("move hydrated bytes")
			}
			if _, err := os.Stat(filepath.Join(point, src)); !os.IsNotExist(err) {
				t.Fatal("source remains", err)
			}
			if interrupt != "" && interrupt != "remote" {
				raw, err := os.ReadFile(filepath.Join(point, dst, "local.txt"))
				if err != nil || string(raw) != "later unsent edit\n" {
					t.Fatal("lost later bytes", err)
				}
				command := exec.Command(cli, "status", dst)
				command.Dir = point
				rawStatus, statusErr := command.CombinedOutput()
				if statusErr != nil {
					t.Fatal(statusErr, string(rawStatus))
				}
				out := string(rawStatus)
				if !strings.Contains(out, "local.txt") {
					t.Fatal("later edit falsely acknowledged", out)
				}
			}
			if raw, err := os.ReadFile(filepath.Join(point, dst, "cloud.txt")); err != nil || string(raw) != "original bytes\n" {
				t.Fatal("fetch new identity", err, string(raw))
			}
			if reader.fetches.Load() != oldFetch+1 {
				t.Fatal("unexpected explicit fetch count", reader.fetches.Load())
			}
			if err := m.waitDownloaded(t.Context(), record, dst+"/cloud.txt"); err != nil {
				t.Fatal("finish adoption", err)
			}
			// Completion cleanup touches metadata only and is idempotent after retirement.
			guards, id, err := guardAnchorMoveState(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := cleanupCompletedTreeMove(t.Context(), record, id); err != nil {
				t.Fatal(err)
			}
			if err := cleanupCompletedTreeMove(t.Context(), record, id); err != nil {
				t.Fatal(err)
			}
			closeAnchorReleaseGuards(guards)
		})
	}
}
