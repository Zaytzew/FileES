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
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestAnchorObserverProofHasIndependentDeadlineAndNoLifecycleLock(t *testing.T) {
	root := t.TempDir()
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("verified fixture\n"), 1024)
	file := filepath.Join(source, "file.txt")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "observer proof", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	id, _ := anchorRemoteIdentity("file.txt", 1, int64(len(data)))
	p := newLocalStateProbe(t, point, file, fmt.Sprintf("f\t%d\t%s\tfile.txt\n", len(data), id))
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	service := &commit.Service{Cli: svn, Anchor: true, RepoURL: uri}
	service.BindWorkingCopy("fixture", point)
	server := ipcserver.New("unused")
	state := server.RegisterRepoAccess("fixture", uri, point, "server", "rw")
	state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < time.Second {
			t.Error("status inherited an expired metadata/proof deadline")
		}
		return service.WithLocalStatus(ctx, point, paths, confirm)
	})
	intent := &sync.Mutex{}
	referenceCalled := false
	m := &anchorManager{helper: p.helper, repos: server.RepoState, localIntentMu: intent,
		newSVN: func(string) (client.Client, error) {
			referenceCalled = true
			if !intent.TryLock() {
				t.Error("pinned reference holds lifecycle lock and blocks moves")
			} else {
				intent.Unlock()
			}
			time.Sleep(2200 * time.Millisecond) // exceed metadata budget, not test timeout
			return svn, nil
		}}
	record := localrepo.Record{ServerID: "server", RepoID: "fixture", RepoURL: uri, LocalPath: point}
	if got, err := m.call(t.Context(), "", "hydrate", "--root", point, "--rel", "file.txt"); err != nil || !got.OK {
		t.Fatal(got, err)
	}
	metadataCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cursor := ""
	if err := m.refreshLocalStatesDuring(metadataCtx, t.Context(), record, &anchorBridge{}, &cursor); err != nil {
		t.Fatal(err)
	}
	proof, err := commit.ReadAnchorIntegrity(point, "file.txt")
	if !referenceCalled || metadataCtx.Err() == nil || err != nil || proof.Phase != "verified" {
		t.Fatalf("independent proof: called=%v metadata=%v proof=%+v err=%v", referenceCalled, metadataCtx.Err(), proof, err)
	}
}

func TestAnchorIntegrityNativeZeroBlocksHoldWithoutOverwritingEdits(t *testing.T) {
	for _, damage := range []bool{false, true} {
		t.Run(fmt.Sprintf("damage=%v", damage), func(t *testing.T) {
			root := t.TempDir()
			cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
			repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
			probeRun(t, root, admin, "create", repo)
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 14574111)
			for i := range data {
				data[i] = byte((i*37+i/4096)%251 + 1)
			}
			file := filepath.Join(source, "OPZ.pdf")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
			probeRun(t, root, cli, "import", "-q", "-m", "integrity fixture", source, uri)
			probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
			if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			id, _ := anchorRemoteIdentity("OPZ.pdf", 1, int64(len(data)))
			p := newLocalStateProbe(t, point, file, fmt.Sprintf("f\t%d\t%s\tOPZ.pdf\n", len(data), id))
			svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
			service := &commit.Service{Cli: svn, Anchor: true, RepoURL: uri}
			service.BindWorkingCopy("fixture", point)
			server := ipcserver.New("unused")
			state := server.RegisterRepoAccess("fixture", uri, point, "server", "rw")
			state.SetAdoptFunc(func(ctx context.Context, rel string, rev int64) error { return service.AdoptPath(ctx, point, rel, rev) })
			m := &anchorManager{helper: p.helper, repos: server.RepoState, newSVN: func(string) (client.Client, error) { return svn, nil }}
			record := localrepo.Record{ServerID: "server", RepoID: "fixture", RepoURL: uri, LocalPath: point}
			answer, err := m.call(t.Context(), "", "hydrate", "--root", point, "--rel", "OPZ.pdf")
			if err != nil || !answer.OK {
				t.Fatal(answer, err)
			}
			target := filepath.Join(point, "OPZ.pdf")
			if !damage {
				unavailable := &anchorManager{helper: m.helper, repos: m.repos, newSVN: func(string) (client.Client, error) { return nil, errors.New("reference offline") }}
				if err := unavailable.verifyAnchorIntegrity(t.Context(), record, id, 1); err == nil {
					t.Fatal("missing reference accepted")
				}
				if err := commit.CheckAnchorIntegrity(point, []string{"OPZ.pdf"}); err == nil {
					t.Fatal("missing reference lost publication fence")
				}
				reader, err := os.Open(target)
				if err != nil {
					t.Fatal(err)
				}
				err = m.verifyAnchorIntegrity(t.Context(), record, id, 1)
				_ = reader.Close()
				if !errors.Is(err, commit.ErrLocalStatusBusy) {
					t.Fatal("busy read must remain pending, not verified/mismatched", err)
				}
				pending, err := commit.ReadAnchorIntegrity(point, "OPZ.pdf")
				if err != nil || pending.Phase != "pending" {
					t.Fatal(pending, err)
				}
			}
			if damage {
				f, err := os.OpenFile(target, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteAt(make([]byte, 16384), 3571712)
				_ = f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if err = m.adopt(t.Context(), record, id, 1); err == nil {
					t.Fatal("unverified bytes adopted")
				}
				receipt, err := commit.ReadAnchorIntegrity(point, "OPZ.pdf")
				if err != nil || receipt.Phase != "held" {
					t.Fatal(receipt, err)
				}
				if err := commit.CheckAnchorIntegrity(point, []string{"OPZ.pdf"}); err == nil {
					t.Fatal("publication fence missing")
				}
				// New manager, no in-memory proof: the durable hold survives.
				restarted := &anchorManager{helper: m.helper, repos: m.repos, newSVN: m.newSVN}
				if err := restarted.adopt(t.Context(), record, id, 1); err == nil {
					t.Fatal("restart bypassed integrity hold")
				}
				expected := bytes.Clone(data)
				clear(expected[3571712:3588096])
				actual, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("local bytes overwritten", err)
				}
				rows, err := svn.Status(t.Context(), point, []string{"OPZ.pdf"})
				if err != nil || len(rows) != 1 || rows[0].Item != "unversioned" {
					t.Fatal("SVN adoption escaped hold", rows, err)
				}
				return
			}
			if err := m.adopt(t.Context(), record, id, 1); err != nil {
				t.Fatal(err)
			}
			meta, err := m.integrityState(t.Context(), record, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, variant := range []string{"file-id", "identity", "usn"} {
				fileID, identity, usn := meta.FileID, id, meta.USN
				if variant == "file-id" {
					fileID = "1:0"
				}
				if variant == "identity" {
					identity = "different identity"
				}
				if variant == "usn" {
					usn = "1"
				}
				reference, err := commit.ReadAnchorIntegrity(point, "OPZ.pdf")
				if err != nil {
					t.Fatal(err)
				}
				answer, err := m.call(t.Context(), "", "verify-download", "--root", point, "--rel", "OPZ.pdf", "--identity", identity, "--file-id", fileID, "--size", fmt.Sprint(len(data)), "--usn", usn, "--sha256", reference.SHA256)
				if err != nil || answer.OK {
					t.Fatal("misbound native proof accepted", variant, answer, err)
				}
			}
			receipt, err := commit.ReadAnchorIntegrity(point, "OPZ.pdf")
			if err != nil || receipt.Phase != "verified" {
				t.Fatal(receipt, err)
			}
			p.accept("OPZ.pdf", true)
			// Editing includes changing EOF; the initial receipt must not be
			// used to overwrite or reject a legitimate later save.
			edited := []byte("legal local edit after verified initial delivery")
			if err := os.WriteFile(target, edited, 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.adopt(t.Context(), record, id, 1); err != nil {
				t.Fatal("subsequent edit rejected", err)
			}
			if err := commit.CheckAnchorIntegrity(point, []string{"OPZ.pdf"}); err != nil {
				t.Fatal(err)
			}
			probeRun(t, point, cli, "commit", "-q", "-m", "publish legitimate subsequent edit", "OPZ.pdf")
			got, err := exec.Command(cli, "cat", uri+"/OPZ.pdf").Output()
			if err != nil || !bytes.Equal(got, edited) {
				t.Fatal("wrong committed bytes", err)
			}
		})
	}
}
