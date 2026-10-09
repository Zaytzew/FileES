//go:build windows && native_cfapi_probe && !nocfapi

package main

// A real Windows/SVN feasibility gate for retained local placeholders. This
// does not enable the experimental Shell mode or change the owner's pair.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"filees/pkg/client"
	"filees/pkg/cloudfiles"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

func probeHandleCount(t *testing.T) uint32 {
	t.Helper()
	var count uint32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	if ok, _, err := proc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count))); ok == 0 {
		t.Fatalf("read probe handles: %v", err)
	}
	return count
}

func TestAnchorLocalStateBatchIncludesRetainedFiles(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "f\t8192\tremote.txt\tremote.txt\n")
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	p.accept("local.txt", true)
	if err := os.WriteFile(filepath.Join(root, "folder", "nested.txt"), []byte("nested bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	p.accept("folder/nested.txt", true)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil {
			t.Logf("entry=%s type=%s info_mode=%s recall=%v", entry.Name(), entry.Type(), info.Mode(), cloudfiles.RecallsOnRead(info))
		}
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			info, e := entry.Info()
			if e == nil {
				t.Logf("walk=%s type=%s mode=%s recall=%v", filepath.Base(path), entry.Type(), info.Mode(), cloudfiles.RecallsOnRead(info))
			}
		}
		return err
	})
	m := &anchorManager{helper: p.helper}
	cursor := ""
	_, paths, err := anchorLocalStateBatch(t.Context(), root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
		answer, err := m.call(t.Context(), strings.Join(paths, "\n")+"\n", "file-states", "--root", root)
		return answer.States, err
	})
	if err != nil || len(paths) != 2 || paths[0] != "folder/nested.txt" || paths[1] != "local.txt" {
		t.Fatalf("retained local file skipped: %v %v", paths, err)
	}
	select {
	case <-p.fetch:
		t.Fatal("retained-file enumeration fetched remote bytes")
	default:
	}
}

func TestAnchorLocalStateLargeTree(t *testing.T) {
	const localCount, remoteCount = 4096, 2048
	root := t.TempDir()
	var listing strings.Builder
	for i := range remoteCount {
		fmt.Fprintf(&listing, "f\t8192\tcloud-%04d.txt\tcloud-%04d.txt\n", i, i)
	}
	p := newLocalStateProbe(t, root, "", listing.String())
	for i := range localCount {
		dir := filepath.Join(root, fmt.Sprintf("local-%02d", i/64))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i)), []byte("synthetic local bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := &anchorManager{helper: p.helper}
	initial := probeHandleCount(t)
	for pass := range 6 {
		cursor := ""
		seen := make(map[string]bool, localCount)
		calls := 0
		started := time.Now()
		var maxBatch time.Duration
		for rounds := 0; ; rounds++ {
			if rounds > localCount/64+2 {
				t.Fatal("local cursor did not finish the pass")
			}
			batchStart := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			states, paths, err := anchorLocalStateBatch(ctx, root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
				calls++
				answer, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "file-states", "--root", root)
				if err != nil || !answer.OK {
					return nil, fmt.Errorf("metadata: %+v %w", answer, err)
				}
				return answer.States, nil
			})
			cancel()
			maxBatch = max(maxBatch, time.Since(batchStart))
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range paths {
				if seen[rel] || !states[rel].OK || states[rel].Partial || !strings.HasPrefix(rel, "local-") {
					t.Fatalf("duplicate, unavailable or remote row: %s", rel)
				}
				seen[rel] = true
			}
			if cursor == "" {
				break
			}
		}
		if len(seen) != localCount || calls != localCount/64 {
			t.Fatalf("local paths=%d helpers=%d", len(seen), calls)
		}
		t.Logf("pass=%d local=%d remote=%d helpers=%d elapsed=%s max_batch=%s handles_initial=%d handles_after=%d goroutines=%d", pass+1, localCount, remoteCount, calls, time.Since(started), maxBatch, initial, probeHandleCount(t), runtime.NumGoroutine())
	}
	select {
	case <-p.fetch:
		t.Fatal("metadata pass fetched remote bytes")
	default:
	}
}

// Exercise the production cadence, real status guard and connected-helper
// ACKs, not just metadata enumeration with the idle intervals removed.
func TestAnchorLargeWorkingPointObserver(t *testing.T) {
	testAnchorWorkingPointObserver(t, 4096)
}

func TestAnchorWorkingPointObserverRepeats(t *testing.T) {
	testAnchorWorkingPointObserver(t, 256)
}

func testAnchorWorkingPointObserver(t *testing.T, localCount int) {
	const remoteCount = 2048
	root := t.TempDir()
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	helper := probeTool(t, "FILEES_CFAPI")
	probeRun(t, root, admin, "create", repo)
	for i := range localCount {
		dir := filepath.Join(source, fmt.Sprintf("local-%02d", i/64))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i)), []byte("synthetic versioned bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "large observer fixture", source, uri)
	probeRun(t, root, cli, "checkout", "-q", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Log("large working copy ready")
	m := &anchorManager{helper: helper}
	if answer, err := m.call(t.Context(), "", "register", "--root", point, "--identity", "synthetic-large-observer"); err != nil || !answer.OK {
		t.Fatalf("register: %+v %v", answer, err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
			t.Errorf("unregister: %s %v", out, err)
		}
	})
	var listing strings.Builder
	for i := range remoteCount {
		fmt.Fprintf(&listing, "f\t8192\tcloud-%04d.txt\tcloud-%04d.txt\n", i, i)
	}
	if answer, err := m.call(t.Context(), listing.String(), "placeholders", "--root", point); err != nil || !answer.OK {
		t.Fatalf("seed: %+v %v", answer, err)
	}
	svn := client.New(client.Options{SvnPath: filepath.Join(root, "absent-cli.exe"), NativeSVNPath: native, Timeout: time.Minute})
	service := &commit.Service{Cli: svn}
	service.BindWorkingCopy("fixture", point)
	// Hold the real writer guard for the first observation, not a fake error.
	writeHeld, releaseWrite, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWrite) }) }
	go func() {
		writerDone <- service.WithWorkingCopyWrite(t.Context(), point, func() error {
			close(writeHeld)
			<-releaseWrite
			return nil
		})
	}()
	<-writeHeld
	t.Cleanup(func() { release(); <-writerDone })
	state := &ipcserver.RepoState{}
	var mu sync.Mutex
	seen := map[string]bool{}
	calls, busy := 0, false
	var lastStatusErr error
	var firstBatch, retryBatch string
	var maxStatus time.Duration
	state.SetLocalStatusFunc(func(ctx context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		mu.Lock()
		calls++
		if !busy {
			busy, firstBatch = true, paths[0]
			mu.Unlock()
			err := service.WithLocalStatus(ctx, point, paths, confirm)
			if err != commit.ErrLocalStatusBusy {
				t.Errorf("real writer did not reject observer: %v", err)
			}
			release()
			return err
		}
		if retryBatch == "" {
			retryBatch = paths[0]
		}
		mu.Unlock()
		started := time.Now()
		err := service.WithLocalStatus(ctx, point, paths, confirm)
		mu.Lock()
		defer mu.Unlock()
		maxStatus = max(maxStatus, time.Since(started))
		lastStatusErr = err
		if err == nil {
			for _, rel := range paths {
				seen[rel] = true
			}
		}
		return err
	})
	m.repos = func(string, string) *ipcserver.RepoState { return state }
	ctx, cancel := context.WithCancel(t.Context())
	streams, err := startAnchorHelper(ctx, helper, point)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	bridge := &anchorBridge{root: point, retainLocal: true, guard: newAnchorGuard(m.log)}
	bridge.observe = func(ctx context.Context, bridge *anchorBridge) {
		defer func() { t.Logf("observer stopped: %v", ctx.Err()) }()
		m.observeLocalStates(ctx, localrepo.Record{LocalPath: point}, bridge)
	}
	done := make(chan error, 1)
	started := time.Now()
	initial := probeHandleCount(t)
	go func() { done <- bridge.serve(ctx, streams) }()
	t.Cleanup(func() { cancel(); streams.abort(); <-done })
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		t.Logf("observer diagnostic: seen=%d calls=%d max_status=%s last_status=%v", len(seen), calls, maxStatus, lastStatusErr)
	}()
	waitFor(t, 2*time.Minute, "all local files through the production observer", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == localCount
	})
	mu.Lock()
	if firstBatch != retryBatch {
		t.Errorf("busy batch skipped: first=%q retry=%q", firstBatch, retryBatch)
	}
	t.Logf("local=%d remote=%d status_batches=%d first_pass_with_busy_backoff=%s max_status_ack=%s handles_initial=%d handles_after=%d goroutines=%d", len(seen), remoteCount, calls, time.Since(started), maxStatus, initial, probeHandleCount(t), runtime.NumGoroutine())
	mu.Unlock()
	// Tail status confirms actual Shell metadata, not merely successful callbacks.
	p := &localStateProbe{t: t, root: point, helper: helper}
	if s := p.state(fmt.Sprintf("local-%02d/file-%04d.txt", (localCount-1)/64, localCount-1)); !s.Local || !s.InSync || s.Partial {
		t.Fatalf("tail state: %+v", s)
	}
	if !cloudfiles.RecallsOnRead(mustStatProbe(t, filepath.Join(point, "cloud-2047.txt"))) {
		t.Fatal("metadata observer downloaded remote bytes")
	}
	// A first conversion of ordinary WC files is more expensive than subsequent
	// checks of retained local placeholders. Measure both with the real cadence.
	mu.Lock()
	seen = map[string]bool{}
	beforeCalls := calls
	maxStatus = 0
	mu.Unlock()
	steadyStarted := time.Now()
	waitFor(t, time.Minute, "a complete retained-local observer pass", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == localCount
	})
	mu.Lock()
	t.Logf("retained_pass=%s status_batches=%d max_status_ack=%s handles_after=%d goroutines=%d", time.Since(steadyStarted), calls-beforeCalls, maxStatus, probeHandleCount(t), runtime.NumGoroutine())
	mu.Unlock()
	tail := fmt.Sprintf("local-%02d/file-%04d.txt", (localCount-1)/64, localCount-1)
	if err := os.WriteFile(filepath.Join(point, filepath.FromSlash(tail)), []byte("synthetic unpublished replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.state(tail).InSync {
		t.Fatal("Windows did not invalidate the changed file")
	}
	if err := service.WithWorkingCopyWrite(t.Context(), point, func() error {
		_, err := svn.Commit(t.Context(), point, []string{tail}, "publish observer fixture edit")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	publicationStarted := time.Now()
	waitFor(t, time.Minute, "the nested published file to be confirmed again", func() bool {
		_, clean := cloudfiles.LocalState(filepath.Join(point, filepath.FromSlash(tail)))
		return clean
	})
	t.Logf("nested_publication_confirmation=%s", time.Since(publicationStarted))
}

func mustStatProbe(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// Opt-in manual receipt. The harness owns the synthetic point and always
// unregisters it; the owner's daemon, configuration and files are untouched.
func TestAnchorShellVisualReceipt(t *testing.T) {
	if os.Getenv("FILEES_CFAPI_VISUAL") != "1" {
		t.Skip("explicit visual receipt only")
	}
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	p := newLocalStateProbe(t, root, "", "f\t8192\tcloud.txt\tcloud.txt\n", true)
	for _, entry := range []struct {
		name  string
		clean bool
	}{{"local-clean.txt", true}, {"local-pending.txt", false}} {
		if err := os.WriteFile(filepath.Join(root, entry.name), []byte("Synthetic FileES visual receipt\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p.accept(entry.name, entry.clean)
	}
	t.Logf("VISUAL_ROOT=%s", root)
	t.Logf("VISUAL_DONE=%s", filepath.Join(root, "visual.done"))
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "visual.done")); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("visual receipt not completed before deadline")
}

type anchorLocalState struct {
	OK, Placeholder, Partial, Local bool
	InSync                          bool `json:"in_sync"`
	USN                             string
}

func TestAnchorConnectedHelperAbortIsReaped(t *testing.T) {
	root := t.TempDir()
	helper := probeTool(t, "FILEES_CFAPI")
	m := &anchorManager{helper: helper}
	if answer, err := m.call(t.Context(), "", "register", "--root", root, "--identity", "synthetic-abort"); err != nil || !answer.OK {
		t.Fatalf("register: %+v %v", answer, err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
			t.Errorf("unregister after abort: %s %v", out, err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	streams, err := startAnchorHelper(ctx, helper, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { streams.abort(); _ = streams.wait(); _ = streams.stdin.Close() })
	reader := bufio.NewScanner(streams.stdout)
	if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
		t.Fatal("connected helper not ready")
	}
	streams.abort() // returns only after the actual child process has exited
	if err := streams.wait(); err == nil {
		t.Fatal("terminated helper unexpectedly reported normal exit")
	}
	if reader.Scan() {
		t.Fatal("connected helper still produced output after being reaped")
	}
}

type localStateProbe struct {
	t      *testing.T
	root   string
	helper string
	input  io.WriteCloser
	mu     sync.Mutex
	reply  chan []string
	fetch  chan []string
	stop   func()
	crash  func() // only this fixture's child provider; kill and reap
}

func newLocalStateProbe(t *testing.T, root, content, listing string, shell ...bool) *localStateProbe {
	t.Helper()
	helper := probeTool(t, "FILEES_CFAPI")
	m := &anchorManager{helper: helper}
	if answer, err := m.call(t.Context(), "", "register", "--root", root, "--identity", "synthetic-local-state"); err != nil || !answer.OK {
		t.Fatalf("register: %+v %v", answer, err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
			t.Errorf("unregister: %s %v", out, err)
		}
	})
	if len(shell) > 0 && shell[0] {
		icon, err := anchorFolderIconPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := markManagedFolder(root, icon); err != nil {
			t.Fatal(err)
		}
		answer, err := m.call(t.Context(), "", "shell-register", "--root", root, "--identity", "synthetic-local-state", "--icon", icon)
		info, _ := m.call(t.Context(), "", "info", "--root", root)
		if err != nil || !answer.OK {
			t.Fatalf("shell registration: %+v %v; info=%+v", answer, err, info)
		}
	}
	if listing != "" {
		if answer, err := m.call(t.Context(), listing, "placeholders", "--root", root); err != nil || !answer.OK {
			t.Fatalf("seed: %+v %v", answer, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	command := exec.CommandContext(ctx, helper, "connect", "--root", root)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewScanner(output)
	if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
		t.Fatal("provider not connected")
	}
	p := &localStateProbe{t: t, root: root, helper: helper, input: input, reply: make(chan []string, 16), fetch: make(chan []string, 16)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for reader.Scan() {
			fields := strings.Split(reader.Text(), "\t")
			if len(fields) >= 5 && fields[0] == "fetch" {
				p.fetch <- fields
				line := "err\t" + fields[1] + "\tunavailable"
				if content != "" {
					line = "ok\t" + fields[1] + "\t" + content
				}
				p.write(line)
			} else if len(fields) == 3 && (fields[0] == "directory-set" || fields[0] == "state-set" || fields[0] == "release-prepared" || fields[0] == "release-restored" || fields[0] == "tree-set") {
				p.reply <- fields
			}
		}
	}()
	var once sync.Once
	p.crash = func() {
		once.Do(func() {
			_ = command.Process.Kill()
			_ = input.Close()
			<-done
			_ = command.Wait()
			cancel()
		})
	}
	p.stop = func() {
		once.Do(func() {
			_ = input.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				cancel()
				<-done
			}
			_ = command.Wait()
			cancel()
		})
	}
	t.Cleanup(p.stop)
	return p
}

func (p *localStateProbe) write(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = io.WriteString(p.input, line+"\n")
}

func (p *localStateProbe) state(rel string) anchorLocalState {
	p.t.Helper()
	raw, err := exec.Command(p.helper, "file-state", "--root", p.root, "--rel", rel).Output()
	var state anchorLocalState
	if json.Unmarshal(raw, &state) != nil || err != nil || !state.OK || state.USN == "" {
		p.t.Fatalf("state %s: %s %v", rel, raw, err)
	}
	return state
}

func (p *localStateProbe) set(rel, usn string, clean bool) string {
	p.t.Helper()
	flag := "0"
	if clean {
		flag = "1"
	}
	p.write("state-set\tprobe\t" + rel + "\t" + usn + "\t" + flag)
	select {
	case fields := <-p.reply:
		return fields[2]
	case <-time.After(10 * time.Second):
		p.t.Fatal("state response timed out")
	}
	return ""
}

func (p *localStateProbe) accept(rel string, clean bool) {
	p.t.Helper()
	if hr := p.set(rel, p.state(rel).USN, clean); hr != hresultOK {
		p.t.Fatalf("set %s: %s", rel, hr)
	}
}

// A Shell checkmark is not permission to discard a versioned file. Until
// release coordinates SVN exclusion and identity restoration, ALWAYS_FULL
// must reject even a direct, exclusive Windows dehydration request.
func TestAnchorRetainedFilesRefuseDirectDehydration(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "")
	path := filepath.Join(root, "retained.txt")
	content := bytes.Repeat([]byte("synthetic published bytes\n"), 512)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	p.accept("retained.txt", true)
	dehydrate := windows.NewLazySystemDLL("cldapi.dll").NewProc("CfDehydratePlaceholder")
	for _, clean := range []bool{true, false} {
		p.accept("retained.txt", clean)
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		// Attribute-only, share-none: no read/hydration and no competing open.
		file, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES|windows.WRITE_DAC,
			0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			t.Fatal(err)
		}
		hr, _, _ := dehydrate.Call(uintptr(file), 0, ^uintptr(0), 0, 0)
		_ = windows.CloseHandle(file)
		// winerror.h: DEHYDRATION_DISALLOWED = 434, NOT_IN_SYNC = 377.
		want := uint32(0x80070000 | 434)
		if !clean {
			want = 0x80070000 | 377
		}
		if uint32(hr) != want {
			t.Fatalf("clean=%v: dehydration result 0x%08x, want 0x%08x", clean, uint32(hr), want)
		}
		state := p.state("retained.txt")
		if state.Partial || !state.Local || state.InSync != clean {
			t.Fatalf("refusal changed state: %+v", state)
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("refusal lost bytes: len=%d err=%v", len(got), err)
		}
	}
	select {
	case request := <-p.fetch:
		t.Fatalf("refusal fetched bytes: %v", request)
	default:
	}
}

func TestAnchorLocalStateRefusesMissingBytesAndStalePublication(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "f\t8192\tremote.txt\tremote.txt\n")
	m := &anchorManager{helper: p.helper}
	// Batch order includes failed rows; an unread placeholder stays off disk.
	batch, err := m.call(t.Context(), "remote.txt\nmissing.txt\n", "file-states", "--root", root)
	if err != nil || !batch.OK || len(batch.States) != 2 || !batch.States[0].Partial || batch.States[1].OK {
		t.Fatalf("batch metadata: %+v %v", batch, err)
	}
	for _, input := range []string{strings.Repeat("remote.txt\n", 65), "remote.txt", strings.Repeat("a", 4096) + "\n"} {
		answer, err := m.call(t.Context(), input, "file-states", "--root", root)
		if err == nil && answer.OK {
			t.Fatal("malformed or oversize batch accepted")
		}
	}
	for _, rel := range []string{"../outside.txt", ".svn/entries", ".svn./entries", ".filees /identity", "local.txt:stream", "/absolute.txt"} {
		if raw, err := exec.Command(p.helper, "file-state", "--root", root, "--rel", rel).CombinedOutput(); err == nil {
			t.Fatalf("invalid relative path accepted: %q %s", rel, raw)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p.accept("empty.txt", true)
	if s := p.state("empty.txt"); !s.Local || s.Partial || !s.InSync {
		t.Fatalf("empty local file: %+v", s)
	}
	remote := p.state("remote.txt")
	if !remote.Partial || hrSuccess(p.set("remote.txt", remote.USN, true)) {
		t.Fatal("unread bytes became a local published file")
	}
	select {
	case <-p.fetch:
		t.Fatal("state inspection hydrated a remote file")
	default:
	}
	path := filepath.Join(root, "local.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	old := p.state("local.txt")
	if err := os.WriteFile(path, []byte("unpublished edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if hrSuccess(p.set("local.txt", old.USN, true)) {
		t.Fatal("stale status marked edited bytes clean")
	}
	p.accept("local.txt", false)
	state := p.state("local.txt")
	if !state.Local || state.Partial || state.InSync {
		t.Fatalf("pending local state: %+v", state)
	}
	p.accept("local.txt", true)
	if !p.state("local.txt").InSync {
		t.Fatal("fresh confirmation was not accepted")
	}
	if local, clean := cloudfiles.LocalState(path); !local || !clean {
		t.Fatalf("Go local state: %v %v", local, clean)
	}
	if err := os.WriteFile(path, []byte("next edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.state("local.txt").InSync {
		t.Fatal("Windows did not invalidate clean after a write")
	}
	stale := p.state("local.txt")
	if err := os.Remove(path); err != nil {
		t.Fatalf("local delete: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if hrSuccess(p.set("local.txt", stale.USN, true)) {
		t.Fatal("replacement accepted old publication")
	}
	p.accept("local.txt", true)
	if err := os.Chmod(path, 0444); err != nil {
		t.Fatal(err)
	}
	p.accept("local.txt", true)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0200 != 0 {
		t.Fatalf("read-only changed: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatalf("local rename: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "renamed.txt")); err != nil || string(got) != "replacement" {
		t.Fatalf("bytes: %q %v", got, err)
	}
	p.stop()
	// Registration and local state survive a provider restart; the test then
	// unregisters it and verifies that local bytes are ordinary and retained.
	if !p.state("renamed.txt").Local {
		t.Fatal("local identity did not survive disconnection")
	}
	if local, _ := cloudfiles.LocalState(filepath.Join(root, "renamed.txt")); !local {
		t.Fatal("Go lost local marker after rename")
	}
	if answer, err := m.call(t.Context(), "", "unregister", "--root", root); err != nil || !answer.OK {
		t.Fatalf("detach: %+v %v", answer, err)
	}
	if cloudfiles.IsPlaceholder(filepath.Join(root, "renamed.txt")) {
		t.Fatal("retained file stayed a placeholder after detach")
	}
	if got, err := os.ReadFile(filepath.Join(root, "renamed.txt")); err != nil || string(got) != "replacement" {
		t.Fatalf("detached bytes: %q %v", got, err)
	}
}

func hrSuccess(hr string) bool { return hr == hresultOK }

func TestAnchorRetainedPlaceholderSupportsRealNativeSVN(t *testing.T) {
	root := t.TempDir()
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("synthetic-dwg-"), 100000)
	cache := filepath.Join(source, "drawing.dwg")
	if err := os.WriteFile(cache, content, 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "fixture", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	p := newLocalStateProbe(t, point, cache, fmt.Sprintf("f\t%d\tdrawing.dwg\tdrawing.dwg\n", len(content)))
	path := filepath.Join(point, "drawing.dwg")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("hydrate: %v", err)
	}
	// Missing CLI forbids silently falling back from the native helper.
	svn := client.New(client.Options{SvnPath: filepath.Join(root, "not-a-cli.exe"), NativeSVNPath: native, Timeout: time.Minute})
	_, err := svn.(interface {
		UpdateAdoptPath(context.Context, string, string, int64) (string, error)
	}).UpdateAdoptPath(t.Context(), point, "drawing.dwg", 1)
	if err != nil {
		t.Fatal(err)
	}
	confirm := func(want string) {
		t.Helper()
		before := p.state("drawing.dwg")
		rows, err := svn.Status(t.Context(), point, []string{"drawing.dwg"})
		if err != nil || len(rows) != 1 || rows[0].Item != want {
			t.Fatalf("native status: %+v %v", rows, err)
		}
		clean := rows[0].Item == "normal" && (rows[0].Props == "none" || rows[0].Props == "normal") && !rows[0].Conflicted
		if hr := p.set("drawing.dwg", before.USN, clean); hr != hresultOK {
			t.Fatalf("set: %s", hr)
		}
		if p.state("drawing.dwg").InSync != clean {
			t.Fatal("Shell does not match SVN")
		}
	}
	confirm("normal")
	if err := os.WriteFile(path, []byte("native publication"), 0600); err != nil {
		t.Fatal(err)
	}
	confirm("modified")
	if _, err := svn.Commit(t.Context(), point, []string{"drawing.dwg"}, "native fixture publication"); err != nil {
		t.Fatal(err)
	}
	confirm("normal")
	// CAD-like replace-on-save, not just an in-place write.
	if err := os.WriteFile(filepath.Join(point, "save.tmp"), []byte("replace-on-save"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(point, "drawing.bak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(point, "save.tmp"), path); err != nil {
		t.Fatal(err)
	}
	confirm("modified")
	if _, err := svn.Commit(t.Context(), point, []string{"drawing.dwg"}, "native replacement"); err != nil {
		t.Fatal(err)
	}
	confirm("normal")
	if got, err := os.ReadFile(path); err != nil || string(got) != "replace-on-save" {
		t.Fatalf("replacement bytes: %q %v", got, err)
	}
}
