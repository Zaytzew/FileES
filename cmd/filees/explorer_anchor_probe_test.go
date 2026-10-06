//go:build native_cfapi_probe

package main

// The whole daemon side of an Explorer anchor against real helpers and a real
// repository, without a server: seed, connect, open a placeholder, adopt it at
// its revision, retain it locally and mirror the confirmed SVN state to Shell.
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
	"filees/pkg/commit"
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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	helper := probeTool(t, "FILEES_CFAPI")
	native := probeTool(t, "FILEES_SVN_PROBE")
	svnCLI := probeTool(t, "FILEES_PROBE_SVN")
	svnadmin := probeTool(t, "FILEES_PROBE_SVNADMIN")

	root := t.TempDir()
	visual := func(stage, anchor string) {
		if os.Getenv("FILEES_CFAPI_VISUAL") != "1" {
			return
		}
		done := filepath.Join(root, "visual-"+stage+".done")
		t.Logf("VISUAL_STAGE=%s VISUAL_ROOT=%s VISUAL_DONE=%s", stage, anchor, done)
		waitFor(t, 3*time.Minute, "visual "+stage, func() bool {
			_, err := os.Stat(done)
			return err == nil
		})
	}
	repo := filepath.Join(root, "repo")
	probeRun(t, root, svnadmin, "create", repo)
	source := filepath.Join(root, "source")
	drawing := bytes.Repeat([]byte("sala-"), 300000) // 1.5 MB, more than one chunk
	model := []byte("model w podfolderze\n")
	if err := os.MkdirAll(filepath.Join(source, "wnętrza-東京"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sala.dwg"), drawing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "wnętrza-東京", "model.blend"), model, 0o644); err != nil {
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
	anchor := filepath.Join(root, "Zażółć-東京-🧪")
	svn := client.New(client.Options{SvnPath: svnCLI, NativeSVNPath: native, Timeout: time.Minute})
	if _, err := svn.(interface {
		CheckoutDepthEmpty(context.Context, string, string) (string, error)
	}).CheckoutDepthEmpty(context.Background(), repoURL, anchor); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(anchor, ".filees"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", anchor).CombinedOutput(); err != nil {
			t.Errorf("unregister: %s (%v)", out, err)
		}
	})

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
	// The production observer uses the commit service's operation guard.
	service := &commit.Service{Cli: svn}
	service.BindWorkingCopy("atlas", anchor)
	state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		return service.WithLocalStatus(ctx, anchor, paths, confirm)
	})
	manager := &anchorManager{
		helper: helper, nativeSVN: native,
		stateEvery: 50 * time.Millisecond,
		repos:      func(string, string) *ipcserver.RepoState { return state },
		newSVN:     func(string) (client.Client, error) { return svn, nil },
		log:        talk.With("anchor-probe"),
		options:    &anchorOptions{},
	}
	manager.options.enabled.Store(true)
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
	for _, rel := range []string{"sala.dwg", "wnętrza-東京", filepath.Join("wnętrza-東京", "model.blend")} {
		if !cloudfiles.IsPlaceholder(filepath.Join(anchor, rel)) {
			t.Fatalf("%s was not seeded as a placeholder", rel)
		}
	}
	info, err := os.Stat(filepath.Join(anchor, "sala.dwg"))
	if err != nil || info.Size() != int64(len(drawing)) || !cloudfiles.RecallsOnRead(info) {
		t.Fatalf("placeholder size/state wrong: %v", err)
	}

	// Opened, as an application would.
	// The seed is durable before connect starts; it is not provider readiness.
	waitFor(t, 10*time.Second, "the connected provider", func() bool {
		info, err := manager.call(ctx, "", "info", "--root", anchor)
		return err == nil && info.OK && info.Status == 1 // CF_PROVIDER_STATUS_IDLE
	})
	visual("online", anchor)
	opened := time.Now()
	// The browser explicitly downloads one file through CFAPI, not SetDepth.
	// Its success must include adoption, even though hydration ACK arrives first.
	if err := manager.downloadFile(t.Context(), record, "sala.dwg"); err != nil {
		t.Fatalf("explicit download: %v", err)
	}
	read, err := os.ReadFile(filepath.Join(anchor, "sala.dwg"))
	if err != nil {
		t.Fatalf("opening the placeholder: %v", err)
	}
	t.Logf("otwarcie sala.dwg (%d B) trwało %s", len(read), time.Since(opened))
	if !bytes.Equal(read, drawing) {
		t.Fatal("the opened file does not hold the repository's bytes")
	}
	nested, err := os.ReadFile(filepath.Join(anchor, "wnętrza-東京", "model.blend"))
	if err != nil || !bytes.Equal(nested, model) {
		t.Fatalf("opening a file in a subfolder: %v", err)
	}

	// Experimental Shell mode keeps the file local instead of stripping its
	// CFAPI metadata. The observer verifies SVN before confirming it clean.
	for _, rel := range []string{"sala.dwg", filepath.Join("wnętrza-東京", "model.blend")} {
		path := filepath.Join(anchor, rel)
		waitFor(t, 30*time.Second, rel+" to become a clean local file", func() bool {
			local, clean := cloudfiles.LocalState(path)
			return local && clean
		})
	}
	statuses, err := svn.Status(context.Background(), anchor, []string{"sala.dwg", "wnętrza-東京/model.blend"})
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
	visual("local", anchor)
	path := filepath.Join(anchor, "sala.dwg")
	if err := os.WriteFile(path, []byte("edited synthetic drawing"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "unpublished local state", func() bool {
		local, clean := cloudfiles.LocalState(path)
		return local && !clean
	})
	visual("changed", anchor)
	if err := manager.downloadFile(t.Context(), record, "sala.dwg"); err != nil {
		t.Fatalf("idempotent download of modified local file: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "edited synthetic drawing" {
		t.Fatalf("explicit download overwrote local work: %q %v", got, err)
	}
	if err := service.WithWorkingCopyWrite(t.Context(), anchor, func() error {
		_, err := svn.Commit(t.Context(), anchor, []string{"sala.dwg"}, "state observer publication")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "confirmed publication", func() bool {
		local, clean := cloudfiles.LocalState(path)
		return local && clean
	})
	visual("published", anchor)
	if err := service.WithWorkingCopyWrite(t.Context(), anchor, func() error {
		_, err := svn.PropSet(t.Context(), anchor, "filees:probe", "changed", []string{"sala.dwg"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "unpublished properties", func() bool {
		local, clean := cloudfiles.LocalState(path)
		return local && !clean
	})
	if err := service.WithWorkingCopyWrite(t.Context(), anchor, func() error {
		_, err := svn.Commit(t.Context(), anchor, []string{"sala.dwg"}, "state observer properties")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "confirmed properties", func() bool {
		local, clean := cloudfiles.LocalState(path)
		return local && clean
	})
	// Reconnect the real provider using the persisted seed, without reseeding
	// or downloading the files again. Both retained bytes and state survive.
	cancel()
	<-stopped
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	stopped = make(chan struct{})
	go func() { manager.keep(ctx, record); close(stopped) }()
	waitFor(t, 10*time.Second, "reconnected provider", func() bool {
		info, err := manager.call(ctx, "", "info", "--root", anchor)
		return err == nil && info.OK && info.Status == 1
	})
	if got, err := os.ReadFile(path); err != nil || string(got) != "edited synthetic drawing" {
		t.Fatalf("reconnected bytes: %q %v", got, err)
	}
	if local, clean := cloudfiles.LocalState(path); !local || !clean {
		t.Fatal("reconnect lost published local state")
	}
	visual("reconnected", anchor)
	t.Logf("visual receipt=%v; synthetic content, not a real CAD drawing", os.Getenv("FILEES_CFAPI_VISUAL") == "1")
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
