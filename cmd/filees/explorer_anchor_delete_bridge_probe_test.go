//go:build windows && native_cfapi_probe && native_cfapi_delete_probe && !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

// Only the fixture may delay/inject frames. Production has one pipe writer.
type deleteProbeWriter struct {
	io.WriteCloser
	intercepted chan struct{}
	release     chan struct{}
	ctx         context.Context
	path        string
}

func (w *deleteProbeWriter) Write(raw []byte) (int, error) {
	f := strings.Split(strings.TrimSpace(string(raw)), "\t")
	if len(f) == 3 && f[0] == "delete-allow" {
		id, _ := strconv.Atoi(f[1])
		for _, line := range []string{"ok\t" + f[1] + "\t" + w.path, "err\t" + f[1] + "\treason", fmt.Sprintf("delete-allow\t%d\t1", id+1), "delete-allow\t0" + f[1] + "\t1", "delete-allow\t" + f[1] + "\t0"} {
			if _, err := io.WriteString(w.WriteCloser, line+"\n"); err != nil {
				return 0, err
			}
		}
		close(w.intercepted)
		select {
		case <-w.release:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	return w.WriteCloser.Write(raw)
}

// Actual connected Cloud Filter -> typed Go bridge -> durable journal -> native
// RA deletion/inspection. This is NOT the final coordinator: authorization and
// scanner selection are isolated fixtures. Selected cases exercise local SVN
// metadata reconciliation; no manifest/scanner CAS or retirement occurs.
func TestAnchorDeleteBridgeNativeDurable(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI_DELETE_PROBE")
	version, err := exec.Command(helper, "version").Output()
	if err != nil || !strings.Contains(string(version), "delete_bridge_probe_v1") {
		t.Fatal("isolated typed bridge helper required", err)
	}
	for _, scenario := range []string{"allow", "lost-reply", "typed-isolation", "metadata-present", "metadata-recreated", "refuse", "default-refuse"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := testTreeMoveIntent(t)
			base := t.TempDir()
			repo, seed := filepath.Join(base, "repo"), filepath.Join(base, "seed")
			cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
			probeRun(t, base, admin, "create", repo)
			if err := os.Mkdir(seed, 0700); err != nil {
				t.Fatal(err)
			}
			source := "plik 新.bin"
			if err := os.WriteFile(filepath.Join(seed, source), []byte("seed"), 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, base, cli, "import", "-q", "-m", "deletion bridge seed", seed, uri)
			svn := client.New(client.Options{SvnPath: filepath.Join(base, "absent-cli.exe"), NativeSVNPath: native, Timeout: time.Minute})
			id, err := svn.(interface {
				HistoryRepositoryUUID(context.Context, string) (string, error)
			}).HistoryRepositoryUUID(t.Context(), uri)
			if err != nil {
				t.Fatal(err)
			}
			r.RepoURL = uri
			request := client.RemoteFileDelete{RootURL: uri, UUID: id, BaseRevision: 1, OperationID: uuid.NewString(), Source: source, Message: "Usunięcie próby"}
			identity, _ := anchorRemoteIdentity(source, 1, 4)
			withMetadata := strings.HasPrefix(scenario, "metadata-")
			if withMetadata {
				probeRun(t, base, cli, "checkout", "-q", "--force", uri, r.LocalPath)
				if err := os.Remove(filepath.Join(r.LocalPath, source)); err != nil {
					t.Fatal(err)
				}
			}
			p := newLocalStateProbe(t, r.LocalPath, "", fmt.Sprintf("f\t4\t%s\t%s\n", identity, source))
			p.stop()
			t.Setenv("FILEES_CFAPI_DELETE_PROBE_BRIDGE", "1")
			// Static characterization approval must not bypass the typed handshake.
			t.Setenv("FILEES_CFAPI_DELETE_PROBE_ALLOW", "1")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			cmd := exec.CommandContext(ctx, helper, "connect", "--root", r.LocalPath)
			out, childOut, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdout = childOut
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			childOut.Close()
			exited := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(exited) }()
			streams := &anchorHelperStreams{stdout: out, stdin: input, abort: func() { cmd.Process.Kill(); <-exited }, wait: func() error { <-exited; out.Close(); return waitErr }}
			var poisoned *deleteProbeWriter
			if scenario == "typed-isolation" {
				poisoned = &deleteProbeWriter{WriteCloser: input, intercepted: make(chan struct{}), release: make(chan struct{}), ctx: ctx, path: filepath.Join(seed, source)}
				streams.stdin = poisoned
			}
			connected := make(chan struct{})
			completed := make(chan error, 1)
			prepared := make(chan error, 1)
			var journal *anchorDeleteSession
			bridge := &anchorBridge{root: r.LocalPath, onConnected: func(context.Context) { close(connected) }}
			var fetches atomic.Int32
			bridge.materializePinned = func(context.Context, string, int64, int64) (string, int64, error) {
				fetches.Add(1)
				return "", 0, errors.New("metadata deletion must not request file bytes")
			}
			if scenario != "default-refuse" {
				bridge.deletePrepare = func(c context.Context, cb anchorDeleteCallback) error {
					if scenario == "refuse" {
						return errors.New("isolated authorization refusal")
					}
					if withMetadata {
						state, err := svn.(client.LocalFileDeleteReconciler).LocalFileDeleteMetadata(c, r.LocalPath, "inspect", request, 0)
						if err != nil || state != "present" {
							return errors.Join(err, errors.New("isolated local metadata preflight failed"))
						}
					}
					journal = newAnchorDeleteSession(r, uint32(cmd.Process.Pid))
					journal.id = cb.Session
					snapshot := &watcher.FileDeleteSnapshot{Schema: "filees.scanner-file-delete/v1", WC: r.LocalPath, Source: source, Session: uuid.NewString(), Sequence: 1, Entry: watcher.PublicationEntry{Path: source}}
					inspect := func(c context.Context) (anchorDeleteFileState, error) {
						return inspectAnchorDeleteFileState(c, p.helper, r.LocalPath, source)
					}
					err := journal.prepare(c, cb, request, snapshot, inspect)
					if err == nil && !commit.HasPendingRemoteFileDelete(r.LocalPath) {
						err = errors.New("permission without durable fence")
					}
					prepared <- err
					return err
				}
				bridge.deleteComplete = func(c context.Context, cb anchorDeleteCallback) error {
					err := journal.complete(c, cb)
					completed <- err
					return err
				}
			}
			done := make(chan error, 1)
			go func() { done <- bridge.serve(ctx, streams) }()
			t.Cleanup(func() {
				cancel()
				streams.abort()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("bridge did not stop")
				}
				if journal != nil {
					journal.close()
				}
			})
			select {
			case <-connected:
			case <-ctx.Done():
				t.Fatal("provider not connected")
			}
			removed := make(chan error, 1)
			go func() { removed <- os.Remove(filepath.Join(r.LocalPath, source)) }()
			if poisoned != nil {
				select {
				case <-poisoned.intercepted:
				case <-ctx.Done():
					t.Fatal("no intercepted permission")
				}
				select {
				case err := <-removed:
					t.Fatal("fetch/foreign/malformed reply granted deletion", err)
				case <-time.After(100 * time.Millisecond):
				}
				close(poisoned.release)
			}
			select {
			case err = <-removed:
			case <-ctx.Done():
				t.Fatal("OS deletion did not finish")
			}
			if scenario == "refuse" || scenario == "default-refuse" {
				if err == nil {
					t.Fatal("refused delete removed placeholder")
				}
				if commit.HasPendingRemoteFileDelete(r.LocalPath) {
					t.Fatal("refusal created an intent")
				}
				select {
				case <-completed:
					t.Fatal("refusal fabricated completion")
				default:
				}
				return
			}
			select {
			case e := <-prepared:
				if e != nil {
					t.Fatal("prepare", e)
				}
			case <-ctx.Done():
				t.Fatal("no preparation")
			}
			if err != nil {
				t.Fatal("Cloud Filter deletion", err)
			}
			select {
			case e := <-completed:
				if e != nil {
					t.Fatal("completion", e)
				}
			case <-ctx.Done():
				t.Fatal("no exact native completion")
			}
			journal.close()
			remote := &anchorDeleteNativeProbe{RemoteFileDeleter: svn.(client.RemoteFileDeleter), lost: scenario == "lost-reply"}
			_, err = sendAnchorFileDelete(ctx, r, remote)
			if (err != nil) != (scenario == "lost-reply") {
				t.Fatal("native result", err)
			}
			receipt, _, err := inspectAnchorFileDelete(ctx, r, remote)
			if err != nil || receipt == nil || receipt.Revision != 2 || receipt.RemoteFileDelete != request {
				t.Fatal(receipt, err)
			}
			if _, err = sendAnchorFileDelete(ctx, r, remote); err == nil || remote.sends != 1 {
				t.Fatal("replayed deletion", err)
			}
			if withMetadata {
				if scenario == "metadata-recreated" {
					if err := os.WriteFile(filepath.Join(r.LocalPath, source), []byte("new unpublished object"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for _, phase := range []string{"stage", "stage", "ack", "ack", "verify"} {
					state, err := svn.(client.LocalFileDeleteReconciler).LocalFileDeleteMetadata(ctx, r.LocalPath, phase, request, receipt.Revision)
					if err != nil || state != "complete" && !(phase == "stage" && state == "scheduled") {
						t.Fatal("connected deletion metadata", phase, state, err)
					}
				}
				if scenario == "metadata-recreated" {
					if got, err := os.ReadFile(filepath.Join(r.LocalPath, source)); err != nil || string(got) != "new unpublished object" {
						t.Fatal("recreated bytes changed", string(got), err)
					}
				}
			}
			if out, err := exec.Command(cli, "cat", "-r", "1", uri+"/"+url.PathEscape(source)+"@1").Output(); err != nil || string(out) != "seed" {
				t.Fatal("historical bytes", string(out), err)
			}
			if !commit.HasPendingRemoteFileDelete(r.LocalPath) {
				t.Fatal("receipt retired unreconciled fence")
			}
			if fetches.Load() != 0 {
				t.Fatal("deletion metadata triggered hydration", fetches.Load())
			}
		})
	}
}
