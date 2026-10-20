//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

// Reproduce a provider/daemon interruption AFTER Windows received all bytes,
// but BEFORE any SVN adoption. No fabricated local marker or NODES edit.
func prehydrateAnchorWithoutAdoption(t *testing.T, m *anchorManager, record localrepo.Record, rel, source string) {
	t.Helper()
	if err := m.prepare(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	streams, err := startAnchorHelper(ctx, m.helper, record.LocalPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	reader := bufio.NewScanner(streams.stdout)
	if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
		cancel()
		t.Fatal("pre-hydration provider did not connect")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for reader.Scan() {
			fields := strings.Split(reader.Text(), "\t")
			if len(fields) >= 5 && fields[0] == "fetch" {
				reply := "err\t" + fields[1] + "\tunexpected fixture path\n"
				if fields[4] == rel {
					reply = "ok\t" + fields[1] + "\t" + source + "\n"
				}
				_, _ = io.WriteString(streams.stdin, reply)
			}
		}
	}()
	answer, hydrateErr := m.call(t.Context(), "", "hydrate", "--root", record.LocalPath, "--rel", rel)
	cancel()
	_ = streams.wait()
	<-done
	if hydrateErr != nil || !answer.OK {
		t.Fatalf("pre-hydration: %+v %v", answer, hydrateErr)
	}
	state, err := m.call(t.Context(), "", "file-state", "--root", record.LocalPath, "--rel", rel)
	if err != nil || !state.OK || state.Partial || state.Local || state.Identity != rel {
		t.Fatalf("not a complete unadopted placeholder: %+v %v", state, err)
	}
	t.Log(fmt.Sprintf("real complete remote fixture without adoption: %s", rel))
}

func TestAnchorRecoveryAdoptPreservesNewerBASEAndLocalEdits(t *testing.T) {
	root := t.TempDir()
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	repo, source, wc := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "wc")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "doc.txt"), []byte("server base"), 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "base r1", source, uri)
	probeRun(t, root, cli, "mkdir", "-q", "-m", "advance to r2", uri+"/later")
	probeRun(t, root, cli, "checkout", "-q", uri, wc)
	path := filepath.Join(wc, "doc.txt")
	if err := os.WriteFile(path, []byte("keep unpublished edits"), 0600); err != nil {
		t.Fatal(err)
	}
	probeRun(t, wc, cli, "propset", "filees:local-probe", "keep property", "doc.txt")
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	service := &commit.Service{Cli: svn}
	service.BindWorkingCopy("fixture", wc)
	if err := service.AdoptPath(t.Context(), wc, "doc.txt", 1); err != nil {
		t.Fatal(err)
	}
	base, err := exec.Command(cli, "info", "--show-item", "revision", path).Output()
	if err != nil || strings.TrimSpace(string(base)) != "2" {
		t.Fatalf("BASE rewound: %s %v", base, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep unpublished edits" {
		t.Fatal("local bytes changed", string(data), err)
	}
	property, err := exec.Command(cli, "propget", "--strict", "filees:local-probe", path).Output()
	if err != nil || string(property) != "keep property" {
		t.Fatal("local property changed", string(property), err)
	}
}

func TestAnchorExplicitDownloadRecoversCompletePinnedFileWithoutFetch(t *testing.T) {
	root := t.TempDir()
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("pinned recovery bytes")
	content := filepath.Join(source, "doc.txt")
	if err := os.WriteFile(content, data, 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "pinned r1", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	id, _ := anchorRemoteIdentity("doc.txt", 1, int64(len(data)))
	p := newLocalStateProbe(t, point, content, fmt.Sprintf("f\t%d\t%s\tdoc.txt\n", len(data), id))
	pre := &anchorManager{helper: p.helper}
	got, err := pre.call(t.Context(), "", "hydrate", "--root", point, "--rel", "doc.txt")
	if err != nil || !got.OK {
		t.Fatal(got, err)
	}
	if state := p.state("doc.txt"); state.Partial || state.Local {
		t.Fatal("not complete remote", state)
	}
	// Consume only the pre-hydration request. The explicit recovery below
	// must not depend on another request, nor download these bytes again.
	select {
	case <-p.fetch:
	case <-time.After(time.Second):
		t.Fatal("missing fixture fetch")
	}
	svn := client.New(client.Options{SvnPath: cli, NativeSVNPath: native, Timeout: time.Minute})
	service := &commit.Service{Cli: svn}
	service.BindWorkingCopy("fixture", point)
	server := ipcserver.New("unused")
	state := server.RegisterRepoAccess("fixture", uri, point, "server", "rw")
	state.SetAdoptFunc(func(ctx context.Context, rel string, rev int64) error { return service.AdoptPath(ctx, point, rel, rev) })
	state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		return service.WithLocalStatus(ctx, point, paths, confirm)
	})
	m := &anchorManager{helper: p.helper, repos: server.RepoState}
	record := localrepo.Record{ServerID: "server", RepoID: "fixture", RepoURL: uri, LocalPath: point}
	if err := m.downloadFile(t.Context(), record, "doc.txt"); err != nil {
		t.Fatal(err)
	}
	rows, err := svn.Status(t.Context(), point, []string{"doc.txt"})
	if err != nil || len(rows) != 1 || rows[0].Item != "normal" {
		t.Fatal("not adopted", rows, err)
	}
	select {
	case request := <-p.fetch:
		t.Fatalf("complete file refetched: %v", request)
	default:
	}
}
