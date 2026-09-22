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
	if got := run(t, "f\t4096\tsala.dwg\tsala.dwg\n", "placeholders", "--root", root); !got.OK {
		t.Fatalf("placeholders: %+v", got)
	}

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
	// The helper says it is live before it reads anything, so waiting for that
	// line is waiting for the anchor, not for a timer.
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	var connected answer
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &connected); err != nil || !connected.OK {
		t.Fatalf("connect: %q %v", line, err)
	}
	if got := run(t, "", "info", "--root", root); !got.OK || !got.Ours || got.Status == 0 {
		t.Fatalf("a connected anchor must report itself as ours and running: %+v", got)
	}

	if err := os.Remove(filepath.Join(root, "sala.dwg")); err == nil {
		t.Fatal("deleting in Explorer would delete in the repository; it must be refused")
	}
	if err := os.Rename(filepath.Join(root, "sala.dwg"), filepath.Join(root, "inna.dwg")); err == nil {
		t.Fatal("renaming in Explorer must be refused")
	}
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
