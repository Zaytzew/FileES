//go:build native_cfapi_probe

package main

// The whole daemon side of an Explorer anchor against real helpers and a real
// repository, without a server: seed, connect, open a placeholder, adopt it at
// its revision, revert it into an ordinary file of the working copy.
//
//	FILEES_CFAPI=<filees-cfapi.exe> FILEES_SVN_PROBE=<filees-svn.exe with sparse_adopt_v1> \
//	FILEES_PROBE_SVN=<svn.exe> FILEES_PROBE_SVNADMIN=<svnadmin.exe> \
//	  go test -tags native_cfapi_probe -run Anchor ./cmd/filees/
//
// Opt-in because it registers a real sync root; it always unregisters it.

import (
	"bufio"
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

func probeTool(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("set %s", key)
	}
	return value
}

func TestAnchorDetachKeepsDownloadedFilesWithoutFetching(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	root := filepath.Join(t.TempDir(), "point")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: helper}
	call := func(stdin string, args ...string) {
		t.Helper()
		answer, err := m.call(t.Context(), stdin, args...)
		if err != nil || !answer.OK {
			t.Fatalf("%v: %+v %v", args, answer, err)
		}
	}
	call("", "register", "--root", root, "--identity", "test\x1frepo")
	t.Cleanup(func() { _ = exec.Command(helper, "unregister", "--root", root).Run() })
	call("d\t0\tfolder\tfolder\nf\t5000000\tunread.dwg\tunread.dwg\n", "placeholders", "--root", root)
	call("f\t7000000\tfolder/unread.bin\tunread.bin\n", "placeholders", "--root", root, "--rel", "folder")
	retained := filepath.Join(root, "folder", "downloaded.txt")
	if err := os.WriteFile(retained, []byte("local changes must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(t.Context())
	command := exec.CommandContext(ctx, helper, "connect", "--root", root)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	t.Cleanup(func() { stop(); _ = input.Close(); <-done })
	ready := make(chan string, 1)
	go func() {
		reader := bufio.NewScanner(output)
		if reader.Scan() {
			ready <- reader.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, `"ok":true`) {
			t.Fatalf("helper not connected: %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper connection timed out")
	}
	record := localrepo.Record{ServerID: "test", RepoID: "repo", LocalPath: root, Anchor: true, State: localrepo.StateDetaching}
	m.running = map[string]anchorConnection{anchorKey(record): {cancel: stop, done: done}}
	if err := m.detach(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(retained); err != nil || string(got) != "local changes must survive" {
		t.Fatalf("retained=%q err=%v", got, err)
	}
	for _, rel := range []string{"unread.dwg", "folder/unread.bin"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("placeholder survived: %s (%v)", rel, err)
		}
	}
	if cloudfiles.IsSyncRoot(root) {
		t.Fatal("sync root still registered")
	}
	if err := m.detach(t.Context(), record); err != nil {
		t.Fatalf("retry after unregister: %v", err)
	}
}

func probeRun(t *testing.T, dir, program string, args ...string) {
	t.Helper()
	command := exec.Command(program, args...)
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(program), args, err, out)
	}
}

func TestAnAnchorOpensAFileAndMakesItPartOfTheWorkingCopy(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	native := probeTool(t, "FILEES_SVN_PROBE")
	svnCLI := probeTool(t, "FILEES_PROBE_SVN")
	svnadmin := probeTool(t, "FILEES_PROBE_SVNADMIN")

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	probeRun(t, root, svnadmin, "create", repo)
	source := filepath.Join(root, "source")
	drawing := bytes.Repeat([]byte("sala-"), 300000) // 1.5 MB, more than one chunk
	model := []byte("model w podfolderze\n")
	if err := os.MkdirAll(filepath.Join(source, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sala.dwg"), drawing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "art", "model.blend"), model, 0o644); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.ToSlash(repo)
	if !strings.HasPrefix(repoPath, "/") {
		repoPath = "/" + repoPath
	}
	repoURL := (&url.URL{Scheme: "file", Path: repoPath}).String()
	probeRun(t, root, svnCLI, "import", "-q", "-m", "init", source, repoURL)

	// What the attach lifecycle leaves behind for an anchor: an empty sparse
	// copy with the working-copy identity directory.
	anchor := filepath.Join(root, "Atlas")
	svn := client.New(client.Options{SvnPath: svnCLI, NativeSVNPath: native, Timeout: time.Minute})
	if _, err := svn.(interface {
		CheckoutDepthEmpty(context.Context, string, string) (string, error)
	}).CheckoutDepthEmpty(context.Background(), repoURL, anchor); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(anchor, ".filees"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(helper, "unregister", "--root", anchor).Run() })

	server := ipcserver.New("unused")
	state := server.RegisterRepoAccess("atlas", repoURL, anchor, "lokalny", "rw")
	adopted := make(chan string, 4)
	state.SetAdoptFunc(func(ctx context.Context, rel string, revision int64) error {
		_, err := svn.(interface {
			UpdateAdoptPath(context.Context, string, string, int64) (string, error)
		}).UpdateAdoptPath(ctx, anchor, rel, revision)
		adopted <- rel
		return err
	})
	manager := &anchorManager{
		helper: helper, nativeSVN: native,
		repos:  func(string, string) *ipcserver.RepoState { return state },
		newSVN: func(string) (client.Client, error) { return svn, nil },
		log:    talk.With("anchor-probe"),
	}
	record := localrepo.Record{ServerID: "lokalny", RepoID: "atlas", RepoURL: repoURL, LocalPath: anchor, Anchor: true, Sparse: true, State: localrepo.StateAttached}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { manager.keep(ctx, record); close(stopped) }()
	t.Cleanup(func() { cancel(); <-stopped })

	// Seeded: the whole tree as placeholders, nothing downloaded.
	waitFor(t, 30*time.Second, "the seed", func() bool {
		_, err := os.Stat(anchorSeedPath(anchor))
		return err == nil
	})
	for _, rel := range []string{"sala.dwg", "art", filepath.Join("art", "model.blend")} {
		if !cloudfiles.IsPlaceholder(filepath.Join(anchor, rel)) {
			t.Fatalf("%s was not seeded as a placeholder", rel)
		}
	}
	info, err := os.Stat(filepath.Join(anchor, "sala.dwg"))
	if err != nil || info.Size() != int64(len(drawing)) || !cloudfiles.RecallsOnRead(info) {
		t.Fatalf("placeholder size/state wrong: %v", err)
	}

	// Opened, as an application would.
	opened := time.Now()
	read, err := os.ReadFile(filepath.Join(anchor, "sala.dwg"))
	if err != nil {
		t.Fatalf("opening the placeholder: %v", err)
	}
	t.Logf("otwarcie sala.dwg (%d B) trwało %s", len(read), time.Since(opened))
	if !bytes.Equal(read, drawing) {
		t.Fatal("the opened file does not hold the repository's bytes")
	}
	nested, err := os.ReadFile(filepath.Join(anchor, "art", "model.blend"))
	if err != nil || !bytes.Equal(nested, model) {
		t.Fatalf("opening a file in a subfolder: %v", err)
	}

	// Then it becomes an ordinary part of the working copy.
	for _, rel := range []string{"sala.dwg", filepath.Join("art", "model.blend")} {
		path := filepath.Join(anchor, rel)
		waitFor(t, 30*time.Second, rel+" to become an ordinary file", func() bool { return !cloudfiles.IsPlaceholder(path) })
	}
	statuses, err := svn.Status(context.Background(), anchor, []string{"sala.dwg", "art/model.blend"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range statuses {
		rel := filepath.ToSlash(entry.Path)
		if strings.Contains(rel, "sala.dwg") || strings.Contains(rel, "model.blend") {
			if entry.Item != "normal" && entry.Item != "" {
				t.Fatalf("%s is %q in the working copy, want clean", rel, entry.Item)
			}
		}
	}
	if len(adopted) < 2 {
		t.Fatalf("adopted %d paths, want both opened files", len(adopted))
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", limit, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
