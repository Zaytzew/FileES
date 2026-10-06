//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
)

func TestAnchorReleaseRecoveryNativeInterruptions(t *testing.T) {
	for _, name := range []string{"normal", "empty", "lost-ack-crash", "cancel-after-effect", "crash-before-effect", "new-work", "creation-race", "wrong-fetch-identity", "wrong-size", "older-observation", "local-work"} {
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
			body := bytes.Repeat([]byte("owned recovery bytes "), 800)
			if name == "empty" {
				body = []byte{}
			}
			cache := filepath.Join(source, "drawing.dwg")
			if err := os.WriteFile(cache, body, 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, root, cli, "import", "-q", "-m", "recovery fixture", source, uri)
			probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
			if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			p := newLocalStateProbe(t, point, cache, fmt.Sprintf("f\t%d\tdrawing.dwg\tdrawing.dwg\n", len(body)))
			path := filepath.Join(point, "drawing.dwg")
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
				t.Fatal("initial hydration")
			}
			svn := client.New(client.Options{NativeSVNPath: native, SvnPath: filepath.Join(root, "absent-cli.exe"), Timeout: time.Minute})
			adopt := svn.(interface {
				UpdateAdoptPath(context.Context, string, string, int64) (string, error)
			})
			if _, err := adopt.UpdateAdoptPath(t.Context(), point, "drawing.dwg", 1); err != nil {
				t.Fatal(err)
			}
			p.accept("drawing.dwg", true)
			if name != "empty" {
				select {
				case <-p.fetch:
				case <-time.After(3 * time.Second):
					t.Fatal("initial hydration receipt missing")
				}
			} // Account for deliberate initial hydration.
			inspection := svn.(anchorReleaseExclusionReader)
			info, err := inspection.InspectPathExclusion(t.Context(), point, "drawing.dwg")
			if err != nil {
				t.Fatal(err)
			}
			identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "fixture", RepoURL: uri, RepoUUID: info.RepositoryUUID, WC: point, Rel: "drawing.dwg", Revision: 1}
			m := &anchorManager{helper: p.helper}
			inspect := func(ctx context.Context, rel string) (cfapiAnswer, error) {
				return m.call(ctx, "", "file-state", "--root", point, "--rel", rel)
			}
			j, err := prepareAnchorReleaseJournal(t.Context(), store, identity, func(ctx context.Context) (cfapiAnswer, error) { return inspect(ctx, identity.Rel) })
			if err != nil {
				t.Fatal(err)
			}
			if err := j.request(t.Context(), "prepare_requested"); err != nil {
				t.Fatal(err)
			}
			if hr := p.prepareRelease(identity.Rel, p.state(identity.Rel).USN); hr != hresultOK {
				t.Fatal(hr)
			}
			if err := j.request(t.Context(), "exclude_requested"); err != nil {
				t.Fatal(err)
			}
			retained := filepath.Join(store, j.id, "data.bin")
			probeRun(t, root, native, "exclude-retained", "--wc", point, "--retained", retained, "--", identity.Rel)
			// From here on neither helper process nor recovery needs the repo.
			if err := os.Rename(repo, repo+"-offline"); err != nil {
				t.Fatal(err)
			}
			newWork := []byte("new work must not be overwritten")
			if name == "new-work" {
				if err := os.WriteFile(path, newWork, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			restores := 0
			ops := anchorReleaseRecoveryIO{svn: inspection, inspect: inspect, restore: func(ctx context.Context, rel string, size int64) error {
				restores++
				if name == "crash-before-effect" {
					p.crash()
					return errors.New("provider exited before effect")
				}
				if name == "creation-race" {
					if err := os.WriteFile(path, newWork, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if name == "wrong-fetch-identity" || name == "wrong-size" {
					p.stop() // Ordinary seed creation needs a disconnected provider.
					fetch, sizeText := rel, fmt.Sprint(size)
					if name == "wrong-fetch-identity" {
						fetch = "different.dwg"
					} else {
						sizeText = fmt.Sprint(size + 1)
					}
					a, err := m.call(ctx, "f\t"+sizeText+"\t"+rel+"\t"+fetch+"\n", "placeholders", "--root", point)
					if err != nil || !a.OK {
						t.Fatalf("seed fixture: %+v %v", a, err)
					}
					p = newLocalStateProbe(t, point, "", "")
					return nil
				}
				if hr := p.restoreRelease(rel, fmt.Sprint(size)); hr != hresultOK {
					return errors.New(hr)
				}
				if name == "lost-ack-crash" || name == "cancel-after-effect" {
					p.crash()
					if name == "cancel-after-effect" {
						cancel()
					}
					return errors.New("receipt deliberately lost after effect")
				}
				return nil
			}}
			if name == "older-observation" {
				ops.inspect = func(ctx context.Context, rel string) (cfapiAnswer, error) {
					a, err := inspect(ctx, rel)
					a.Identity = ""
					a.Size = nil
					return a, err
				}
			}
			if name == "local-work" {
				if err := j.request(t.Context(), "restore_requested"); err != nil {
					t.Fatal(err)
				}
				if hr := p.restoreRelease(identity.Rel, fmt.Sprint(len(body))); hr != hresultOK {
					t.Fatal(hr)
				}
				if err := os.WriteFile(path, newWork, 0600); err != nil {
					t.Fatal(err)
				}
				p.accept(identity.Rel, false)
				// The deliberate user write may hydrate before recovery starts.
				select {
				case <-p.fetch:
				default:
				}
			}
			out, err := j.recoverSeed(ctx, ops)
			good := name == "normal" || name == "empty" || name == "lost-ack-crash"
			if good && (err != nil || !out.seedObserved) {
				t.Fatalf("recovery: %+v %v", out, err)
			}
			if !good && (err == nil || out.seedObserved) {
				t.Fatalf("unsafe recovery: %+v %v", out, err)
			}
			if name == "lost-ack-crash" || name == "cancel-after-effect" || name == "crash-before-effect" {
				p = newLocalStateProbe(t, point, "", "")
				ops.inspect = inspect
				ops.restore = func(ctx context.Context, rel string, size int64) error {
					restores++
					if hr := p.restoreRelease(rel, fmt.Sprint(size)); hr != hresultOK {
						return errors.New(hr)
					}
					return nil
				}
			}
			if good || name == "cancel-after-effect" || name == "crash-before-effect" {
				// New object, new provider after a crash, no source deletion retry.
				fresh := &anchorReleaseJournal{store: store, identity: identity, id: j.id}
				if out, err := fresh.recoverSeed(t.Context(), ops); err != nil || !out.seedObserved {
					t.Fatalf("fresh recovery: %+v %v", out, err)
				}
				want := 1
				if name == "crash-before-effect" {
					want = 2
				}
				if restores != want {
					t.Fatalf("blind duplicate creation: %d want %d", restores, want)
				}
			}
			if name == "new-work" || name == "creation-race" || name == "local-work" {
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, newWork) {
					t.Fatal("new work lost")
				}
			}
			if got, err := os.ReadFile(retained); err != nil || !bytes.Equal(got, body) {
				t.Fatal("retained bytes lost")
			}
			if r, err := j.inspect(t.Context()); err != nil || r.Identity != identity {
				t.Fatalf("journal lost: %+v %v", r, err)
			}
			select {
			case request := <-p.fetch:
				t.Fatalf("recovery hydrated data: %v", request)
			default:
			}
		})
	}
}
