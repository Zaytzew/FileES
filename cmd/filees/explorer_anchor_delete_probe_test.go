//go:build windows && native_cfapi_probe && native_cfapi_delete_probe && !nocfapi

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This characterizes Windows, NOT reception of a durable product delete flow.
// Only the differently named, opt-in executable may allow these deletions.
func TestAnchorRemoteDeleteCallbackNative(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI_DELETE_PROBE")
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow-%t", allow), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("FILEES_CFAPI_DELETE_PROBE_ALLOW", "1")
			t.Setenv("FILEES_CFAPI_DELETE_PROBE_BRIDGE", "1")
			p := newLocalStateProbe(t, root, "", "d\t0\tfolder\tfolder\nf\t8192\tremote 新.bin\tremote 新.bin\n")
			standard, err := exec.Command(p.helper, "version").Output()
			if err != nil || strings.Contains(string(standard), "delete_callback_probe_v1") || strings.Contains(string(standard), "delete_bridge_probe_v1") {
				t.Fatal("probe leaked into product helper", err)
			}
			// A product provider ignores the probe's environment variable.
			if err := os.Remove(filepath.Join(root, "remote 新.bin")); err == nil {
				t.Fatal("product helper allowed remote deletion")
			}
			p.stop()
			if a, err := (&anchorManager{helper: p.helper}).call(t.Context(), "f\t8192\tfolder/nested.bin\tnested.bin\n", "placeholders", "--root", root, "--rel", "folder"); err != nil || !a.OK {
				t.Fatal("nested seed", a, err)
			}
			version, err := exec.Command(helper, "version").Output()
			if err != nil || !strings.Contains(string(version), "delete_callback_probe_v1") {
				t.Fatal("isolated probe required", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, helper, "connect", "--root", root)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(strings.ToUpper(value), "FILEES_CFAPI_DELETE_PROBE_ALLOW=") && !strings.HasPrefix(strings.ToUpper(value), "FILEES_CFAPI_DELETE_PROBE_BRIDGE=") {
					cmd.Env = append(cmd.Env, value)
				}
			}
			if allow {
				cmd.Env = append(cmd.Env, "FILEES_CFAPI_DELETE_PROBE_ALLOW=1")
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewScanner(output)
			if !reader.Scan() || !strings.Contains(reader.Text(), `"ok":true`) {
				t.Fatal("probe not connected")
			}
			frames := make(chan []string, 16)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for reader.Scan() {
					f := strings.Split(reader.Text(), "\t")
					select {
					case frames <- f:
					case <-ctx.Done():
						return
					}
				}
			}()
			defer func() {
				_ = input.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					cancel()
					<-done
				}
				_ = cmd.Wait()
			}()
			read := func() []string {
				t.Helper()
				select {
				case f := <-frames:
					if len(f) != 10 || f[0] != "delete-probe" {
						t.Fatal("unexpected frame (including hydration)", f)
					}
					return f
				case <-time.After(3 * time.Second):
					t.Fatal("missing callback frame")
				}
				return nil
			}
			for _, rel := range []string{"remote 新.bin", "folder/nested.bin"} {
				path := filepath.Join(root, filepath.FromSlash(rel))
				wide, _ := windows.UTF16PtrFromString(path)
				h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
					nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if err != nil {
					t.Fatal(err)
				}
				var info windows.ByHandleFileInformation
				err = windows.GetFileInformationByHandle(h, &info)
				_ = windows.CloseHandle(h)
				if err != nil {
					t.Fatal(err)
				}
				err = os.Remove(path)
				if allow && err != nil {
					t.Fatal("probe delete", err)
				}
				if !allow && err == nil {
					t.Fatal("probe refusal ignored")
				}
				pre := read()
				if pre[1] != "pre" || pre[2] != strconv.FormatUint(uint64(info.VolumeSerialNumber), 10) ||
					pre[4] != strconv.FormatUint(uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow), 10) ||
					pre[7] != rel || pre[9] != strconv.Itoa(os.Getpid()) {
					t.Fatal("pre-delete not bound to actual object/process", pre)
				}
				if allow {
					complete := read()
					for _, field := range []int{2, 3, 4, 7, 8, 9} {
						if complete[field] != pre[field] {
							t.Fatal("completion binding changed", field, pre, complete)
						}
					}
					if complete[1] != "complete" {
						t.Fatal("completion missing", complete)
					}
					t.Logf("%s: request pre=%s complete=%s; normalized path=%q; flags pre=%s complete=%s", rel, pre[5], complete[5], pre[8], pre[6], complete[6])
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatal("completed deletion did not remove object", err)
					}
				} else {
					// os.Remove can try again after access denial (including the
					// Windows read-only-attribute fallback). More pre callbacks
					// are not successful completions or fresh deletion intents.
					drained := false
					for !drained {
						select {
						case frame := <-frames:
							if len(frame) != 10 || frame[0] != "delete-probe" || frame[1] != "pre" || frame[4] != pre[4] || frame[7] != pre[7] {
								t.Fatal("refused deletion produced completion or unrelated frame", frame)
							}
						case <-time.After(100 * time.Millisecond):
							drained = true
						}
					}
					if _, err := os.Lstat(path); err != nil {
						t.Fatal("refusal removed object", err)
					}
				}
			}
		})
	}
}
