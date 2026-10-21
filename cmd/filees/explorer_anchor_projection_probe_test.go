//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
	"golang.org/x/sys/windows"
)

func TestAnchorRecoveryRemoveNativePinsObjectAndExcludesWriters(t *testing.T) {
	root := t.TempDir()
	identity, _ := anchorRemoteIdentity("remote.bin", 1, 4096)
	p := newLocalStateProbe(t, root, "", fmt.Sprintf("f\t4096\t%s\tremote.bin\n", identity))
	state, err := inspectAnchorDeleteFileState(t.Context(), p.helper, root, "remote.bin")
	if err != nil {
		t.Fatal(err)
	}
	remove := func(id, usn string) string {
		t.Helper()
		p.write(strings.Join([]string{"recovery-remove", "probe", "remote.bin", identity, "4096", id, usn}, "\t"))
		select {
		case reply := <-p.reply:
			return reply[2]
		case <-time.After(12 * time.Second):
			t.Fatal("bound remove ACK timeout")
			return ""
		}
	}
	if hr := remove("0:1", state.USN); hr == hresultOK {
		t.Fatal("foreign FileID removed")
	}
	if hr := remove(state.FileID, "1"); hr == hresultOK {
		t.Fatal("foreign USN removed")
	}
	path, _ := windows.UTF16PtrFromString(filepath.Join(root, "remote.bin"))
	h, err := windows.CreateFile(path, windows.FILE_APPEND_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	hr := remove(state.FileID, state.USN)
	windows.CloseHandle(h)
	if hr == hresultOK {
		t.Fatal("concurrent writer was not excluded")
	}
	after, err := inspectAnchorDeleteFileState(t.Context(), p.helper, root, "remote.bin")
	if err != nil || after.FileID != state.FileID || !anchorDeleteRemoteState(after, identity) {
		t.Fatal("refusal changed placeholder", after, err)
	}
	if hr := remove(after.FileID, after.USN); hr != hresultOK {
		t.Fatal("exact remove", hr)
	}
	if _, err := os.Lstat(filepath.Join(root, "remote.bin")); !os.IsNotExist(err) {
		t.Fatal("exact object remains", err)
	}
	select {
	case fetch := <-p.fetch:
		t.Fatal("bound remove requested hydration", fetch)
	default:
	}
}

// Real SVN creates and retains its own file-remove work item. Neither fixture
// nor product inserts/deletes SQL rows. Only a disposable point is registered.
func TestAnchorSVNWorkQueueProjectionNative(t *testing.T) {
	for _, scenario := range []string{"resume", "lost-ack", "replacement", "busy", "restore-obstruction"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			base := t.TempDir()
			repository, seed, point := filepath.Join(base, "repository"), filepath.Join(base, "seed"), filepath.Join(base, "point")
			cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
			probeRun(t, base, admin, "create", repository)
			if err := os.Mkdir(seed, 0700); err != nil {
				t.Fatal(err)
			}
			const rel = "mobile/Camera/photo 新.bin"
			const second = "mobile/Camera/second.bin"
			if err := os.MkdirAll(filepath.Join(seed, "mobile", "Camera"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seed, rel), []byte("seed"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seed, second), []byte("seed"), 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repository)}).String()
			probeRun(t, base, cli, "import", "-q", "-m", "isolated seed", seed, uri)
			probeRun(t, base, cli, "checkout", "-q", uri, point)
			original, err := os.Stat(filepath.Join(point, rel))
			if err != nil {
				t.Fatal(err)
			}
			secondOriginal, err := os.Stat(filepath.Join(point, second))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(point, rel)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(point, second)); err != nil {
				t.Fatal(err)
			}
			for _, directory := range []string{filepath.Join(point, "mobile", "Camera"), filepath.Join(point, "mobile")} {
				if err := os.Remove(directory); err != nil {
					t.Fatal("empty fixture directory", err)
				}
			}
			if err := os.MkdirAll(filepath.Join(point, ".filees", "state"), 0700); err != nil {
				t.Fatal(err)
			}
			identity, _ := anchorRemoteIdentity(rel, 1, 4)
			secondIdentity, _ := anchorRemoteIdentity(second, 1, 4)
			mobileID, _ := anchorRemoteIdentity("mobile", 1, 0)
			cameraID, _ := anchorRemoteIdentity("mobile/Camera", 1, 0)
			p := newLocalStateProbe(t, point, "", fmt.Sprintf("d\t0\t%s\tmobile\n", mobileID))
			for _, command := range []string{
				strings.Join([]string{"tree-set", "fixture", "mobile/Camera", "d", "0", "", cameraID, "0"}, "\t"),
				strings.Join([]string{"tree-set", "fixture", rel, "f", "4", "", identity, "0"}, "\t"),
				strings.Join([]string{"tree-set", "fixture", second, "f", "4", "", secondIdentity, "0"}, "\t"),
			} {
				p.write(command)
				select {
				case reply := <-p.reply:
					if reply[2] != hresultOK {
						t.Fatal("nested seed", reply)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			// Avoid a content comparison when generating the interrupted task:
			// restore only the recorded fixture timestamp, not file bytes.
			if err := os.Chtimes(filepath.Join(point, rel), original.ModTime(), original.ModTime()); err != nil {
				t.Fatal(err)
			}
			metadata, err := inspectAnchorDeleteFileState(ctx, p.helper, point, rel)
			if err != nil {
				t.Fatal(err)
			}
			p.write(strings.Join([]string{"stream-set", "fixture", rel, metadata.USN, metadata.FileID, identity, "4"}, "\t"))
			select {
			case reply := <-p.reply:
				if reply[2] != hresultOK {
					t.Fatal("fixture timestamp ACK", reply)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := os.Chtimes(filepath.Join(point, second), secondOriginal.ModTime(), secondOriginal.ModTime()); err != nil {
				t.Fatal(err)
			}
			secondState, err := inspectAnchorDeleteFileState(ctx, p.helper, point, second)
			if err != nil {
				t.Fatal(err)
			}
			p.write(strings.Join([]string{"stream-set", "fixture", second, secondState.USN, secondState.FileID, secondIdentity, "4"}, "\t"))
			select {
			case reply := <-p.reply:
				if reply[2] != hresultOK {
					t.Fatal("second fixture timestamp ACK", reply)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cmd := exec.CommandContext(ctx, cli, "update", "--set-depth", "empty", "--revision", "1", point)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("old unbound deletion unexpectedly succeeded: %s", output)
			}
			t.Logf("original file-remove refusal: %s", output)
			select {
			case fetch := <-p.fetch:
				t.Fatalf("task creation requested hydration: %v", fetch)
			default:
			}
			p.stop()
			countQueue := func() string {
				t.Helper()
				command := exec.CommandContext(ctx, probeTool(t, "FILEES_PROBE_SQLITE"), "-readonly", filepath.Join(point, ".svn", "wc.db"), "PRAGMA query_only=ON; SELECT count(*) FROM WORK_QUEUE; SELECT count(*) FROM WC_LOCK;")
				raw, err := command.Output()
				if err != nil {
					t.Fatal("readonly queue counts", err)
				}
				return strings.Join(strings.Fields(string(raw)), "/")
			}
			if got := countQueue(); got != "4/1" {
				t.Fatal("fixture did not retain the expected SVN queue/lock", got)
			}
			authority := "svn+ssh://_filees-client@fixture/11111111-1111-4111-8111-111111111111"
			store, err := localrepo.Open(filepath.Join(base, "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := store.BeginAnchorAttach("lab", "repo", point, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.ApproveAttach(r.OperationID, "lab", "repo", authority, "rw"); err != nil {
				t.Fatal(err)
			}
			r, err = store.MarkAttached(r.OperationID, "repo")
			if err != nil {
				t.Fatal(err)
			}
			ipc := ipcserver.New("unused")
			state := ipc.RegisterRepoAccess("repo", authority, point, "lab", "rw")
			m := &anchorManager{helper: p.helper, lifecycle: store, repos: ipc.RepoState, localIntentMu: &sync.Mutex{}, options: &anchorOptions{}, log: talk.With("projection-probe")}
			m.options.servers.Store(map[string]bool{"lab": true})
			guards, root, err := guardAnchorMoveState(r)
			if err != nil {
				t.Fatal(err)
			}
			closeAnchorReleaseGuards(guards)
			tree := anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: r.OperationID, URL: authority, Root: root, Revision: 1,
				Nodes: []anchorRemoteNode{{HistoryTreeNode: client.HistoryTreeNode{Path: rel, Kind: "file", Size: 4}, Identity: identity}}}
			tree.Nodes = append(tree.Nodes, anchorRemoteNode{HistoryTreeNode: client.HistoryTreeNode{Path: second, Kind: "file", Size: 4}, Identity: secondIdentity})
			for _, path := range []string{"mobile", "mobile/Camera"} {
				id, _ := anchorRemoteIdentity(path, 1, 0)
				tree.Nodes = append(tree.Nodes, anchorRemoteNode{HistoryTreeNode: client.HistoryTreeNode{Path: path, Kind: "dir"}, Identity: id})
			}
			if err = saveAnchorRemoteTree(r, tree); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, p.helper, "connect", "--root", point)
			out, childOut, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stdout = childOut
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			childOut.Close()
			exited := make(chan struct{})
			var waitErr error
			go func() { waitErr = command.Wait(); close(exited) }()
			streams := &anchorHelperStreams{stdout: out, stdin: input, abort: func() { command.Process.Kill(); <-exited }, wait: func() error { <-exited; out.Close(); return waitErr }}
			connected := make(chan struct{})
			var fetches atomic.Int32
			bridge := &anchorBridge{root: point, onConnected: func(context.Context) { close(connected) },
				materializePinned: func(context.Context, string, int64, int64) (string, int64, error) {
					fetches.Add(1)
					return "", 0, errors.New("no hydration allowed")
				}}
			done := make(chan error, 1)
			go func() { done <- bridge.serve(ctx, streams) }()
			t.Cleanup(func() {
				cancel()
				streams.abort()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("provider bridge did not stop")
				}
			})
			select {
			case <-connected:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			m.running = map[string]anchorConnection{anchorKey(r): {root: point, repoURL: authority, ctx: ctx, providerCtx: ctx, bridge: bridge}}
			cfg := config.Repo{ID: "repo", ServerID: "lab", RepoURL: authority, LocalPath: point, Anchor: true}
			lost := true
			replacedRel := ""
			var calls int
			svn := client.New(client.Options{NativeSVNPath: native, Timeout: time.Minute, ProjectionRemove: func(c context.Context, request client.ProjectionRemoval) error {
				calls++
				if scenario == "replacement" {
					replacedRel = request.Path
					st, err := inspectAnchorDeleteFileState(c, p.helper, point, request.Path)
					if err != nil {
						return err
					}
					if err := bridge.connectedCommand(c, "recovery-remove", "tree-set", request.Path, st.Identity, "4", st.FileID, st.USN); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(point, request.Path), []byte("keep local replacement"), 0600); err != nil {
						return err
					}
				}
				if err := m.projectSVNWorkItem(c, cfg, request); err != nil {
					return err
				}
				if scenario == "lost-ack" && lost {
					lost = false
					return errors.New("isolated lost reply after exact physical removal")
				}
				return nil
			}, ProjectionComplete: func(c context.Context, wc string) error {
				if scenario == "restore-obstruction" {
					if err := os.WriteFile(filepath.Join(point, rel), []byte("keep local restore obstruction"), 0600); err != nil {
						return err
					}
				}
				return m.completeSVNWorkItems(c, cfg)
			}})
			var recoveryPhase string
			service := &commit.Service{Cli: svn, Anchor: true, RepoURL: uri, RequireSVNMetadata: true, Logger: talk.With("replay-probe"),
				OnWorkingCopyRecovery: func(p *contract.WorkingCopyRecoveryStatus) {
					recoveryPhase = ""
					if p != nil {
						recoveryPhase = p.Phase
					}
				}}
			service.BindWorkingCopy("repo", point)
			state.SetLocalStatusFunc(func(c context.Context, paths []string, apply func([]client.StatusEntry) error) error {
				return service.WithLocalStatus(c, point, paths, apply)
			})
			reader := anchorBatchProbeReader{Client: svn, actual: uri, authority: authority}
			m.newSVN = func(string) (client.Client, error) { return reader, nil }
			if scenario == "busy" {
				m.localIntentMu.Lock()
			}
			_, err = service.RecoverWorkingCopy(ctx, point)
			if scenario == "restore-obstruction" {
				if err == nil || !commit.HasPendingWorkingCopyRecovery(point) || recoveryPhase != "blocked" || countQueue() != "0/0" {
					t.Fatal("view restoration failure was declared complete", recoveryPhase, err)
				}
				b, readErr := os.ReadFile(filepath.Join(point, rel))
				if readErr != nil || string(b) != "keep local restore obstruction" {
					t.Fatal("local restore obstruction lost", readErr)
				}
				return
			}
			if scenario == "busy" {
				m.localIntentMu.Unlock()
				if err == nil || !commit.IsWorkingCopyRecoveryWaiting(err) || recoveryPhase != "waiting" {
					t.Fatal("local contention did not wait", recoveryPhase, err)
				}
				_, err = service.RecoverWorkingCopy(ctx, point)
			}
			if scenario == "replacement" {
				if calls != 1 {
					t.Fatal("replacement callback did not run", calls, err)
				}
				if err == nil || !commit.HasPendingWorkingCopyRecovery(point) {
					t.Fatal("replacement did not hold", err)
				}
				b, readErr := os.ReadFile(filepath.Join(point, replacedRel))
				if readErr != nil || string(b) != "keep local replacement" {
					t.Fatal("replacement lost", readErr)
				}
				return
			}
			if scenario == "lost-ack" {
				if err == nil || !commit.HasPendingWorkingCopyRecovery(point) {
					t.Fatal("lost ACK was accepted", err)
				}
				// A new service/process invocation resumes the very same SVN task.
				service = &commit.Service{Cli: svn, Anchor: true, RepoURL: uri, RequireSVNMetadata: true, Logger: talk.With("replay-resume-probe")}
				service.BindWorkingCopy("repo", point)
				_, err = service.RecoverWorkingCopy(ctx, point)
			}
			wantCalls := 2
			if scenario == "busy" {
				wantCalls = 3
			}
			if err != nil || calls != wantCalls || commit.HasPendingWorkingCopyRecovery(point) {
				t.Fatal("replay did not finish", calls, err)
			}
			if got := countQueue(); got != "0/0" {
				t.Fatal("SVN work queue/lock survived positive completion", got)
			}
			for _, path := range []string{rel, second} {
				st, inspectErr := inspectAnchorDeleteFileState(ctx, p.helper, point, path)
				id, _ := anchorRemoteIdentity(path, 1, 4)
				if inspectErr != nil || !anchorDeleteRemoteState(st, id) {
					t.Fatal("virtual view not restored before clearing fence", path, st, inspectErr)
				}
			}
			hints, err := loadAnchorRemoteTreeAt(r, root, anchorReprojectionPath(point))
			if err != nil || len(hints.Nodes) != 4 || hints.Projection == "" {
				t.Fatal("reprojection evidence lost", hints, err)
			}
			// Same server revision still contains this path. The actual observer
			// must consume the durable hint, not just a manually issued create.
			m.refreshRemoteTree(ctx, r, bridge, &anchorTreeRefresh{})
			st, err := inspectAnchorDeleteFileState(ctx, p.helper, point, rel)
			if err != nil || !anchorDeleteRemoteState(st, identity) || fetches.Load() != 0 {
				t.Fatal("reprojection hydrated/changed data", st, err, fetches.Load())
			}
			completedHints, err := loadAnchorRemoteTreeAt(r, root, anchorReprojectionPath(point))
			if err != nil || completedHints.Projection != "" || len(completedHints.Nodes) != 0 {
				t.Fatal("observer did not retire acknowledged hint", completedHints, err)
			}
			// A partially finished observer must not retire a newer callback's
			// evidence, even when that callback selected the same path again.
			hints.Projection = "newer-operation"
			if err = saveAnchorRemoteTreeAt(r, hints, anchorReprojectionPath(point)); err != nil {
				t.Fatal(err)
			}
			current, err := loadAnchorRemoteTree(r, root)
			if err != nil {
				t.Fatal(err)
			}
			m.refreshRemoteTree(ctx, r, bridge, &anchorTreeRefresh{target: &current, reprojection: "older-operation"})
			kept, err := loadAnchorRemoteTreeAt(r, root, anchorReprojectionPath(point))
			if err != nil || kept.Projection != "newer-operation" {
				t.Fatal("older observer lost newer evidence", kept, err)
			}
			check := exec.CommandContext(ctx, cli, "info", "--show-item", "revision", uri)
			raw, err := check.Output()
			if err != nil || strings.TrimSpace(string(raw)) != "1" {
				t.Fatal("local replay made a remote commit", string(raw), err)
			}
		})
	}
}
