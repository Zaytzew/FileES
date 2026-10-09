//go:build native_cfapi_probe && windows

package nativecfapiprobe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func moveID(t *testing.T, path string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d:%d", info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow))
}

func TestProviderMovesTreeAndRetargetsWithoutFetching(t *testing.T) {
	root := anchor(t)
	src, dst := "źródło", "cel"
	oldRoot, oldFile := "filees.remote/v1:2:0:"+src, "filees.remote/v1:2:17:"+src+"/cloud.txt"
	if got := run(t, "d\t0\t"+oldRoot+"\t"+src+"\n", "placeholders", "--root", root); !got.OK {
		t.Fatal(got)
	}
	if got := run(t, "f\t17\t"+oldFile+"\tcloud.txt\n", "placeholders", "--root", root, "--rel", src); !got.OK {
		t.Fatal(got)
	}
	local := filepath.Join(root, src, "local.txt")
	if err := os.WriteFile(local, []byte("local unsent bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rootID, fileID, localID := moveID(t, filepath.Join(root, src)), moveID(t, filepath.Join(root, src, "cloud.txt")), moveID(t, local)
	content := filepath.Join(t.TempDir(), "remote-body.txt")
	if err := os.WriteFile(content, []byte("remote file body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helper(t), "connect", "--root", root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var connected answer
	if err := json.Unmarshal([]byte(line), &connected); err != nil || !connected.OK {
		t.Fatalf("connect: %s %v", line, err)
	}
	replies := make(chan string, 16)
	var fetches atomic.Int32
	newFile := "filees.remote/v1:3:17:" + dst + "/cloud.txt"
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Split(strings.TrimSpace(line), "\t")
			if len(fields) >= 5 && fields[0] == "fetch" {
				fetches.Add(1)
				response := "err\t" + fields[1] + "\tunrelated identity"
				if fields[4] == newFile {
					response = "ok\t" + fields[1] + "\t" + content
				}
				_, _ = io.WriteString(stdin, response+"\n")
			} else if len(fields) == 3 {
				replies <- strings.TrimSpace(line)
			}
		}
	}()
	command := func(line string, ok bool) {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-replies:
			if strings.HasSuffix(got, "\t0x00000000") != ok {
				t.Fatalf("command %s: %s", line, got)
			}
		case <-time.After(12 * time.Second):
			t.Fatalf("command timed out: %s", line)
		}
	}
	if err := os.Rename(filepath.Join(root, src), filepath.Join(root, "foreign")); err == nil {
		t.Fatal("ordinary client renamed remote source")
	}
	command("tree-move\tbad\t"+src+"\t"+dst+"\t0:1", false)
	command("tree-move\tmove\t"+src+"\t"+dst+"\t"+rootID, true)
	command("tree-identity\troot\t"+dst+"\t"+rootID+"\t"+oldRoot+"\tfilees.remote/v1:3:0:"+dst, true)
	command("tree-identity\tfile\t"+dst+"/cloud.txt\t"+fileID+"\t"+oldFile+"\t"+newFile, true)
	// Exact replay only changes identity when necessary.
	command("tree-identity\treplay\t"+dst+"/cloud.txt\t"+fileID+"\t"+oldFile+"\t"+newFile, true)
	if fetches.Load() != 0 {
		t.Fatalf("move or metadata retarget fetched %d files", fetches.Load())
	}
	if moveID(t, filepath.Join(root, dst)) != rootID || moveID(t, filepath.Join(root, dst, "cloud.txt")) != fileID || moveID(t, filepath.Join(root, dst, "local.txt")) != localID {
		t.Fatal("objects replaced")
	}
	if body, err := os.ReadFile(filepath.Join(root, dst, "local.txt")); err != nil || string(body) != "local unsent bytes\n" {
		t.Fatalf("local bytes changed: %v %q", err, body)
	}
	if body, err := os.ReadFile(filepath.Join(root, dst, "cloud.txt")); err != nil || string(body) != "remote file body\n" {
		t.Fatalf("new identity fetch: %v %q", err, body)
	}
	if fetches.Load() != 1 {
		t.Fatalf("expected one explicit read fetch, got %d", fetches.Load())
	}
}
