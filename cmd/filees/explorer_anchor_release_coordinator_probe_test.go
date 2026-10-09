//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/watcher"
)

func (p *localStateProbe) prepareReleaseBound(rel, usn string, source anchorReleaseFileID) string {
	p.t.Helper()
	p.write(fmt.Sprintf("prepare-release-bound\tprobe\t%s\t%s\t%d:%d", rel, usn, source.Volume, source.Index))
	select {
	case fields := <-p.reply:
		if fields[0] != "release-prepared" || fields[1] != "probe" {
			p.t.Fatalf("wrong bound receipt: %v", fields)
		}
		return fields[2]
	case <-time.After(3 * time.Second):
		p.t.Fatal("bound preparation timed out")
	}
	return ""
}

type releaseProbeExcluder struct {
	anchorReleaseExcluder
	exclude func(context.Context, string, string, string, client.RetainedPathBinding) error
}

func (p releaseProbeExcluder) ExcludeRetainedPath(ctx context.Context, wc, rel, retained string, b client.RetainedPathBinding) error {
	return p.exclude(ctx, wc, rel, retained, b)
}

func (p releaseProbeExcluder) RequireRetainedResumeSupport(ctx context.Context) error {
	r, ok := p.anchorReleaseExcluder.(anchorReleaseResumer)
	if !ok {
		return errors.New("fixture runtime lacks recovery")
	}
	return r.RequireRetainedResumeSupport(ctx)
}

func (p releaseProbeExcluder) RequireRetainedRetrySupport(ctx context.Context) error {
	r, ok := p.anchorReleaseExcluder.(anchorReleaseRetrySupport)
	if !ok {
		return errors.New("fixture runtime lacks pre-effect recovery")
	}
	return r.RequireRetainedRetrySupport(ctx)
}

func (p releaseProbeExcluder) ResumeRetainedPath(ctx context.Context, wc, rel, retained string, b client.RetainedPathBinding) error {
	r, ok := p.anchorReleaseExcluder.(anchorReleaseResumer)
	if !ok {
		return errors.New("fixture runtime lacks recovery")
	}
	return r.ResumeRetainedPath(ctx, wc, rel, retained, b)
}

type retirementProbeRemote struct {
	anchorReleaseRemoteReader
	bad string
}

func (p retirementProbeRemote) InspectRetainedRemote(ctx context.Context, target string) (client.RetainedRemoteIdentity, error) {
	v, err := p.anchorReleaseRemoteReader.InspectRetainedRemote(ctx, target)
	if p.bad == "uuid" {
		v.RepositoryUUID = "11111111-1111-1111-1111-111111111111"
	}
	return v, err
}

func (p retirementProbeRemote) HistoryFetchFile(ctx context.Context, target string, revision int64, out string) (int64, error) {
	n, err := p.anchorReleaseRemoteReader.HistoryFetchFile(ctx, target, revision, out)
	if err == nil && p.bad == "bytes" {
		b, e := os.ReadFile(out)
		if e != nil {
			return n, e
		}
		if len(b) == 0 {
			b = []byte("other")
		} else {
			b[0] ^= 32
		}
		err = os.WriteFile(out, b, 0600)
	}
	return n, err
}

// Actual CFAPI and SVN in owned fixtures. No production pair, GUI or copy retirement.
func TestAnchorReleaseCoordinatorNative(t *testing.T) {
	for _, name := range []string{"normal", "empty", "managed", "managed-empty", "managed-ack-failure", "lost-prepare-ack", "lost-exclude-ack", "exclude-refused", "exclude-refused-source-changed", "cancel-after-prepare", "cancel-after-exclude", "defer-after-base", "crash-after-base", "prepare-refused", "same-bytes-replacement", "wrong-revision", "wrong-uuid", "wrong-url", "old-runtime", "old-cfapi", "native-unprepared"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo, source, point, store := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point"), filepath.Join(root, "evidence")
			cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
			probeRun(t, root, admin, "create", repo)
			for _, dir := range []string{source, store} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			body := bytes.Repeat([]byte("bound retained fixture "), 800)
			if name == "empty" || name == "managed-empty" {
				body = []byte{}
			}
			cache := filepath.Join(source, "drawing.dwg")
			if err := os.WriteFile(cache, body, 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, root, cli, "import", "-q", "-m", "release fixture", source, uri)
			probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
			if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			p := newLocalStateProbe(t, point, cache, fmt.Sprintf("f\t%d\tdrawing.dwg\tdrawing.dwg\n", len(body)))
			path := filepath.Join(point, "drawing.dwg")
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
				t.Fatal("initial hydration")
			}
			c := client.New(client.Options{NativeSVNPath: native, SvnPath: filepath.Join(root, "absent-cli.exe"), Timeout: time.Minute})
			adopt := c.(interface {
				UpdateAdoptPath(context.Context, string, string, int64) (string, error)
			})
			if _, err := adopt.UpdateAdoptPath(t.Context(), point, "drawing.dwg", 1); err != nil {
				t.Fatal(err)
			}
			p.accept("drawing.dwg", true)
			if len(body) > 0 {
				select {
				case <-p.fetch:
				case <-time.After(3 * time.Second):
					t.Fatal("hydration receipt")
				}
			}
			svn := c.(anchorReleaseExcluder)
			info, err := svn.InspectPathExclusion(t.Context(), point, "drawing.dwg")
			if err != nil {
				t.Fatal(err)
			}
			identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "fixture", RepoURL: uri, RepoUUID: info.RepositoryUUID, WC: point, Rel: "drawing.dwg", Revision: 1}
			m := &anchorManager{helper: p.helper}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prepares, excludes, restores := 0, 0, 0
			ops := anchorReleaseIO{helper: filepath.Clean(p.helper), svn: svn, inspect: func(ctx context.Context, rel string) (cfapiAnswer, error) {
				return m.call(ctx, "", "file-state", "--root", point, "--rel", rel)
			},
				prepare: func(ctx context.Context, rel, usn string, id anchorReleaseFileID) error {
					prepares++
					if name == "prepare-refused" {
						id.Index++
					}
					if hr := p.prepareReleaseBound(rel, usn, id); hr != hresultOK {
						return errors.New(hr)
					}
					if name == "cancel-after-prepare" {
						cancel()
					}
					if name == "lost-prepare-ack" {
						return errors.New("lost prepare receipt")
					}
					return nil
				}, restore: func(ctx context.Context, rel string, size int64) error {
					restores++
					if hr := p.restoreRelease(rel, fmt.Sprint(size)); hr != hresultOK {
						return errors.New(hr)
					}
					return nil
				}}
			ops.svn = releaseProbeExcluder{anchorReleaseExcluder: svn, exclude: func(ctx context.Context, wc, rel, retained string, b client.RetainedPathBinding) error {
				excludes++
				if (name == "exclude-refused" || name == "exclude-refused-source-changed") && excludes == 1 {
					return errors.New("fixture: native admission refused before BASE transition")
				}
				if name == "defer-after-base" || name == "crash-after-base" {
					mode := "defer"
					if name == "crash-after-base" {
						mode = "crash"
					}
					out, err := exec.CommandContext(ctx, probeTool(t, "FILEES_SVN_RELEASE_PROBE"), path, retained, mode).CombinedOutput()
					if err == nil {
						t.Fatalf("probe did not stop: %s", out)
					}
					return err
				}
				switch name {
				case "same-bytes-replacement":
					if err := os.Rename(path, path+".saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
				case "wrong-revision":
					b.Revision++
				case "wrong-uuid":
					b.RepositoryUUID = "11111111-1111-1111-1111-111111111111"
				case "wrong-url":
					b.URL += "-other"
				}
				err := svn.ExcludeRetainedPath(ctx, wc, rel, retained, b)
				if name == "cancel-after-exclude" && err == nil {
					cancel()
				}
				if name == "lost-exclude-ack" && err == nil {
					return errors.New("lost exclusion receipt")
				}
				return err
			}}
			if name == "old-runtime" {
				old := client.New(client.Options{NativeSVNPath: probeTool(t, "FILEES_SVN_UNMODIFIED_PROBE"), SvnPath: filepath.Join(root, "absent-cli.exe"), Timeout: time.Minute})
				ops.svn = old.(anchorReleaseExcluder)
			}
			if name == "old-cfapi" {
				ops.helper = filepath.Clean(probeTool(t, "FILEES_CFAPI_OLD_RELEASE_PROBE"))
			}
			if name == "native-unprepared" {
				j, err := prepareAnchorReleaseJournal(t.Context(), store, identity, func(ctx context.Context) (cfapiAnswer, error) { return ops.inspect(ctx, identity.Rel) })
				if err != nil {
					t.Fatal(err)
				}
				r, err := j.inspect(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				u, err := anchorReleaseURL(r)
				if err != nil {
					t.Fatal(err)
				}
				binding := client.RetainedPathBinding{SourceVolume: r.Source.Volume, SourceIndex: r.Source.Index, RepositoryUUID: identity.RepoUUID, URL: u, Revision: 1}
				if err := svn.ExcludeRetainedPath(t.Context(), point, identity.Rel, filepath.Join(store, j.id, "data.bin"), binding); err == nil {
					t.Fatal("unprepared placeholder excluded")
				}
				if state := p.state(identity.Rel); !state.Placeholder || !state.Local {
					t.Fatal("unprepared placeholder mutated", state)
				}
				if got, err := svn.InspectPathExclusion(t.Context(), point, identity.Rel); err != nil || got.Excluded || !got.Idle {
					t.Fatal("BASE changed", got, err)
				}
				return
			}
			if name == "managed" || name == "managed-empty" || name == "managed-ack-failure" {
				state := filepath.Join(point, ".filees", "state")
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				scanner, err := watcher.NewScanner(watcher.Options{WC: point, StatePath: filepath.Join(state, "manifest.json"), PublicationPending: func() bool { return commit.HasPendingFileRelease(point) }})
				if err != nil {
					t.Fatal(err)
				}
				service := &commit.Service{Cli: c, FenceFileRelease: scanner.WithCleanFileMutation}
				service.BindWorkingCopy("fixture", point)
				var saved *anchorReleaseJournal
				var fence *anchorReleaseFence
				ops.retained = func(ctx context.Context, j *anchorReleaseJournal) error {
					saved = j
					var err error
					fence, err = j.persistFence(ctx)
					return err
				}
				remote := c.(anchorReleaseRemoteReader)
				ackFailure := errors.New("fixture durable ack refused")
				if name == "managed-ack-failure" {
					service.FenceFileRelease = func(_ context.Context, _ string, op func() error) error {
						if err := op(); err != nil {
							return err
						}
						return ackFailure
					}
				}
				err = service.WithFileRelease(ctx, point, identity.Rel, false, func() error {
					var err error
					var result anchorReleaseRecovery
					saved, result, err = startAnchorRelease(ctx, store, identity, ops)
					if err == nil && !result.seedObserved {
						return errors.New("missing remote seed")
					}
					return err
				}, func() error { return finishManagedAnchorRelease(ctx, saved, fence, ops, remote) })
				if name == "managed-ack-failure" {
					if !errors.Is(err, ackFailure) || !commit.HasPendingFileRelease(point) {
						t.Fatal("ack failure cleared fence", err)
					}
					if got, err := os.ReadFile(filepath.Join(store, saved.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
						t.Fatal("ack failure lost retained bytes", err)
					}
					service.FenceFileRelease = scanner.WithCleanFileMutation
					err = service.WithFileRelease(t.Context(), point, identity.Rel, true, func() error {
						var err error
						saved, fence, err = loadAnchorReleaseFence(t.Context(), store, identity)
						if err != nil {
							return err
						}
						return advanceManagedAnchorRelease(t.Context(), saved, ops, remote)
					}, func() error { return finishManagedAnchorRelease(t.Context(), saved, fence, ops, remote) })
				}
				if err != nil || commit.HasPendingFileRelease(point) {
					t.Fatal("managed cycle", err)
				}
				if _, err := os.Lstat(filepath.Join(store, saved.id, "data.bin")); !os.IsNotExist(err) {
					t.Fatal("verified retained copy not retired", err)
				}
				if _, err := os.Stat(filepath.Join(state, "manifest.json")); err != nil {
					t.Fatal("scanner ack not durable", err)
				}
				select {
				case request := <-p.fetch:
					t.Fatalf("release hydrated bytes: %v", request)
				default:
				}
				return
			}
			j, out, err := startAnchorRelease(ctx, store, identity, ops)
			good := name == "normal" || name == "empty" || name == "lost-prepare-ack" || name == "lost-exclude-ack"
			if good && (err != nil || !out.seedObserved) {
				t.Fatalf("cycle: %+v %v", out, err)
			}
			if !good && (err == nil || out.seedObserved) {
				t.Fatalf("unsafe cycle: %+v %v", out, err)
			}
			if name == "old-runtime" || name == "old-cfapi" {
				if j != nil || prepares != 0 || excludes != 0 {
					t.Fatal("old runtime mutated")
				}
				return
			}
			if j == nil {
				t.Fatal("missing retained journal", err)
			}
			if got, err := os.ReadFile(filepath.Join(store, j.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
				t.Fatal("retained bytes lost")
			}
			if name == "exclude-refused-source-changed" {
				changed := append([]byte(nil), body...)
				changed[0] ^= 32
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "exclude-refused" || name == "exclude-refused-source-changed" || name == "cancel-after-prepare" || name == "cancel-after-exclude" || name == "defer-after-base" || name == "crash-after-base" || name == "prepare-refused" || name == "same-bytes-replacement" || name == "wrong-revision" || name == "wrong-uuid" || name == "wrong-url" {
				fresh := &anchorReleaseJournal{store: store, identity: identity, id: j.id}
				out, err = fresh.advanceRelease(t.Context(), ops)
				want := name == "exclude-refused" || name == "cancel-after-prepare" || name == "cancel-after-exclude" || name == "defer-after-base" || name == "crash-after-base"
				if want && (err != nil || !out.seedObserved) {
					t.Fatalf("fresh resume: %+v %v", out, err)
				}
				if !want && (err == nil || out.seedObserved) {
					t.Fatalf("unsafe resume: %+v %v", out, err)
				}
			}
			maxExcludes := 1
			if name == "exclude-refused" || name == "wrong-revision" || name == "wrong-uuid" || name == "wrong-url" {
				maxExcludes = 2 // Fresh admission, never another prepare or generic cleanup.
			}
			if prepares != 1 || excludes > maxExcludes || restores > 1 {
				t.Fatalf("blind retry: %d/%d/%d", prepares, excludes, restores)
			}
			if name == "exclude-refused" && (excludes != 2 || restores != 1) {
				t.Fatal("pre-effect refusal did not complete exactly once")
			}
			if name == "exclude-refused-source-changed" {
				changed := append([]byte(nil), body...)
				changed[0] ^= 32
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, changed) || excludes != 1 || restores != 0 {
					t.Fatal("changed source was retried or damaged", err)
				}
			}
			if name == "same-bytes-replacement" || name == "wrong-revision" || name == "wrong-uuid" || name == "wrong-url" {
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
					t.Fatal("refused source lost")
				}
			}
			if got, err := exec.CommandContext(t.Context(), cli, "cat", uri+"/drawing.dwg").Output(); err != nil || !bytes.Equal(got, body) {
				t.Fatal("repository bytes changed", err)
			}
			if got, err := exec.CommandContext(t.Context(), cli, "info", "--show-item", "revision", uri).Output(); err != nil || string(bytes.TrimSpace(got)) != "1" {
				t.Fatal("repository history changed", got)
			}
			select {
			case request := <-p.fetch:
				t.Fatalf("release hydrated data: %v", request)
			default:
			}
			if name == "normal" || name == "empty" {
				remote := c.(anchorReleaseRemoteReader)
				recovery := anchorReleaseRecoveryIO{svn: ops.svn, inspect: ops.inspect, restore: ops.restore}
				for _, bad := range []string{"uuid", "bytes"} {
					if err := j.retireCopy(t.Context(), recovery, retirementProbeRemote{anchorReleaseRemoteReader: remote, bad: bad}); err == nil {
						t.Fatal("unsafe retirement", bad)
					}
					if got, err := os.ReadFile(filepath.Join(store, j.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
						t.Fatal("refused retirement lost copy")
					}
				}
				if err := j.retireCopy(t.Context(), recovery, remote); err != nil {
					t.Fatal("retirement", err)
				}
				if _, err := os.Stat(filepath.Join(store, j.id, "data.bin")); !os.IsNotExist(err) {
					t.Fatal("verified copy not retired", err)
				}
				fresh := &anchorReleaseJournal{store: store, identity: identity, id: j.id}
				if err := fresh.retireCopy(t.Context(), recovery, remote); err != nil {
					t.Fatal("retirement receipt replay", err)
				}
				// A subsequent same-content object is not the retired copy.
				if err := os.WriteFile(filepath.Join(store, j.id, "data.bin"), body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := fresh.retireCopy(t.Context(), recovery, remote); err == nil {
					t.Fatal("completed retirement accepted replacement")
				}
				if got, err := os.ReadFile(filepath.Join(store, j.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
					t.Fatal("replacement copy lost")
				}
			}
		})
	}
}
