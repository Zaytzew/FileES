//go:build native_cfapi_probe

// Opt-in suite for the Explorer anchor helper. It registers a real sync root
// in a temporary folder, so it runs only when asked for by tag, and it fails
// rather than skips when the helper is missing: a skip here reads like an
// acceptance.
package nativecfapiprobe

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type answer struct {
	Schema   string `json:"schema"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Error    string `json:"error"`
	HResult  string `json:"hresult"`
	Provider string `json:"provider"`
	Ours     bool   `json:"ours"`
	Status   int    `json:"status"`
}

func helper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Fatal("the Explorer anchor helper is Windows only")
	}
	path := strings.TrimSpace(os.Getenv("FILEES_CFAPI"))
	if path == "" {
		t.Fatal("set FILEES_CFAPI to the built filees-cfapi.exe")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("FILEES_CFAPI: %v", err)
	}
	return path
}

func run(t *testing.T, stdin string, args ...string) answer {
	t.Helper()
	command := exec.Command(helper(t), args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	raw, err := command.Output()
	var decoded answer
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &decoded); jsonErr != nil {
		t.Fatalf("%v: %v (output %q)", args, jsonErr, raw)
	}
	if err != nil && decoded.OK {
		t.Fatalf("%v: exit %v with ok:true", args, err)
	}
	return decoded
}

// anchor registers one folder and always unregisters it, because a sync root
// left behind outlives this test, this process and this reboot.
func anchor(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "kotwica")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "", "register", "--root", root, "--identity", "office\x1frepo"); !got.OK {
		t.Fatalf("register: %+v", got)
	}
	t.Cleanup(func() { run(t, "", "unregister", "--root", root) })
	return root
}

func TestAnchorShowsHeadNamesWithoutDownloadingAnything(t *testing.T) {
	root := anchor(t)
	listing := "d\t0\tart\tart\nf\t524288\tsala.dwg\tsala.dwg\n"
	if got := run(t, listing, "placeholders", "--root", root); !got.OK {
		t.Fatalf("placeholders: %+v", got)
	}
	if got := run(t, "f\t2048\tart/model.blend\tmodel.blend", "placeholders", "--root", root, "--rel", "art"); !got.OK {
		t.Fatalf("placeholders in a subdirectory: %+v", got)
	}
	info, err := os.Stat(filepath.Join(root, "sala.dwg"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 524288 {
		t.Fatalf("Explorer must show the size from HEAD, got %d", info.Size())
	}
	if _, err := os.Stat(filepath.Join(root, "art", "model.blend")); err != nil {
		t.Fatalf("nested placeholder: %v", err)
	}
	// Nothing was downloaded: the file reads as a placeholder, not as bytes.
	if _, err := os.ReadFile(filepath.Join(root, "sala.dwg")); err == nil {
		t.Fatal("reading a placeholder must not succeed while nothing is connected")
	}
}

func TestAListingIsDataAndIsCheckedAsSuch(t *testing.T) {
	root := anchor(t)
	for _, listing := range []string{
		"f\t10\tid\t..\\poza.txt\n",
		"f\t10\tid\tsub\\dir.txt\n",
		"f\t10\tid\t..\n",
		"nonsense\n",
	} {
		if got := run(t, listing, "placeholders", "--root", root); got.OK {
			t.Fatalf("listing %q was accepted", listing)
		}
	}
}

func TestAConnectedAnchorRefusesDeletingAndRenaming(t *testing.T) {
	root := anchor(t)
	if got := run(t, "f\t4096\tprojekt/sala.dwg\tsala.dwg\n", "placeholders", "--root", root); !got.OK {
		t.Fatalf("placeholders: %+v", got)
	}
	// Deleting a placeholder makes Windows hydrate it first, so the bridge has
	// to be answered even here; a refusal is enough.
	connectAnchor(t, root, func(request []string) string { return "err\t" + request[1] + "\tnot for this test" })
	if got := run(t, "", "info", "--root", root); !got.OK || !got.Ours || got.Status == 0 {
		t.Fatalf("a connected anchor must report itself as ours and running: %+v", got)
	}

	started := time.Now()
	if err := os.Remove(filepath.Join(root, "sala.dwg")); err == nil {
		t.Fatal("deleting in Explorer would delete in the repository; it must be refused")
	}
	t.Logf("kasowanie odmowione po %s", time.Since(started))
	started = time.Now()
	if err := os.Rename(filepath.Join(root, "sala.dwg"), filepath.Join(root, "inna.dwg")); err == nil {
		t.Fatal("renaming in Explorer must be refused")
	}
	t.Logf("zmiana nazwy odmowiona po %s", time.Since(started))
	if _, err := os.Stat(filepath.Join(root, "sala.dwg")); err != nil {
		t.Fatalf("the refused operations must leave the placeholder alone: %v", err)
	}
}

func TestInfoTellsAnAnchorFromAnOrdinaryFolder(t *testing.T) {
	plain := t.TempDir()
	if got := run(t, "", "info", "--root", plain); got.OK {
		t.Fatalf("an ordinary folder must not report as an anchor: %+v", got)
	}
	root := anchor(t)
	got := run(t, "", "info", "--root", root)
	if !got.OK || !got.Ours || got.Provider != "FileES" {
		t.Fatalf("info: %+v", got)
	}
}

func TestPathsAndVerbsAreCheckedBeforeAnythingIsRegistered(t *testing.T) {
	for _, args := range [][]string{
		{"register", "--root", "kotwica", "--identity", "x"},
		{"register", "--root", filepath.Join(os.TempDir(), "kotwica")},
		{"nonsense", "--root", os.TempDir()},
		{"placeholders", "--root", os.TempDir(), "--rel", "..\\poza"},
	} {
		if got := run(t, "", args...); got.OK {
			t.Fatalf("%v was accepted", args)
		}
	}
}

// anchorProcess is the daemon's side of the bridge, played by the test: it
// starts `connect`, answers fetch requests from a table, and records what was
// asked for. The helper never learns it is not talking to the daemon.
type anchorProcess struct {
	t        *testing.T
	command  *exec.Cmd
	stdin    io.WriteCloser
	requests chan []string
}

func connectAnchor(t *testing.T, root string, answerFetch func(request []string) string) *anchorProcess {
	t.Helper()
	command := exec.Command(helper(t), "connect", "--root", root)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	anchor := &anchorProcess{t: t, command: command, stdin: stdin, requests: make(chan []string, 8)}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	var connected answer
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &connected); err != nil || !connected.OK {
		t.Fatalf("connect: %q %v", line, err)
	}
	go func() {
		for {
			raw, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Split(strings.TrimSpace(raw), "\t")
			if len(fields) < 5 || fields[0] != "fetch" {
				continue
			}
			anchor.requests <- fields
			if reply := answerFetch(fields); reply != "" {
				_, _ = io.WriteString(stdin, reply+"\n")
			}
		}
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
		}
	})
	return anchor
}

func TestOpeningAPlaceholderHandsBackTheWorkingCopysBytes(t *testing.T) {
	root := anchor(t)
	content := make([]byte, 3*1024*1024+17) // more than one chunk, ending off a page boundary
	for i := range content {
		content[i] = byte(i % 251)
	}
	materialized := filepath.Join(t.TempDir(), "sala.dwg")
	if err := os.WriteFile(materialized, content, 0o644); err != nil {
		t.Fatal(err)
	}
	listing := "f\t" + itoa(len(content)) + "\tprojekt/sala.dwg\tsala.dwg\n"
	if got := run(t, listing, "placeholders", "--root", root); !got.OK {
		t.Fatalf("placeholders: %+v", got)
	}

	anchored := connectAnchor(t, root, func(request []string) string {
		if request[4] != "projekt/sala.dwg" {
			return "err\t" + request[1] + "\tunknown identity"
		}
		return "ok\t" + request[1] + "\t" + materialized
	})

	read, err := os.ReadFile(filepath.Join(root, "sala.dwg"))
	if err != nil {
		t.Fatalf("opening a placeholder must give the file: %v", err)
	}
	if len(read) != len(content) {
		t.Fatalf("got %d bytes, want %d", len(read), len(content))
	}
	for i := range read {
		if read[i] != content[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
	select {
	case request := <-anchored.requests:
		// The daemon is asked for the path inside the repository, which is what
		// the placeholder was created with - never for a path on this disk.
		if request[4] != "projekt/sala.dwg" {
			t.Fatalf("the daemon was asked for %q", request[4])
		}
	default:
		t.Fatal("no fetch reached the daemon")
	}

	// Once hydrated, the file is ordinary: reading it again asks nobody.
	drain(anchored.requests)
	if _, err := os.ReadFile(filepath.Join(root, "sala.dwg")); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-anchored.requests:
		t.Fatalf("a hydrated file must not be fetched again: %v", request)
	default:
	}
}

func TestADaemonThatRefusesLeavesTheFileUnopenedRatherThanEmpty(t *testing.T) {
	root := anchor(t)
	if got := run(t, "f\t4096\tprojekt/brak.dwg\tbrak.dwg\n", "placeholders", "--root", root); !got.OK {
		t.Fatalf("placeholders: %+v", got)
	}
	connectAnchor(t, root, func(request []string) string {
		return "err\t" + request[1] + "\tnie ma takiej sciezki"
	})
	read, err := os.ReadFile(filepath.Join(root, "brak.dwg"))
	if err == nil {
		t.Fatalf("a refused fetch must fail the open, got %d bytes", len(read))
	}
}

func drain(requests chan []string) {
	for {
		select {
		case <-requests:
		default:
			return
		}
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
