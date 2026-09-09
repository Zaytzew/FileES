//go:build native_svn_probe && windows

package client

import (
	"context"
	"errors"
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

// The actual C helper launches this synthetic SSH transport. Its descendant
// deliberately inherits stderr, reproducing a pipe held open beyond C exit.
func TestNativeTunnelFixture(t *testing.T) {
	root := os.Getenv("FILEES_CANCEL_FIXTURE")
	if root == "" {
		return
	}
	if strings.Contains(strings.Join(os.Args, " "), "tunnel-grandchild") {
		if err := os.WriteFile(filepath.Join(root, "grandchild"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(91)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	exe, _ := os.Executable()
	child := exec.Command(exe, "-test.run=^TestNativeTunnelFixture$", "--", "tunnel-grandchild")
	// Only stderr survives SSH exit; stdout must close so SVN sees protocol
	// EOF and can finish its refusal instead of legitimately awaiting a reply.
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(92)
	}
	if err := os.WriteFile(filepath.Join(root, "ssh"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(93)
	}
	if os.Getenv("FILEES_CANCEL_FIXTURE_EXIT") == "1" {
		for i := 0; i < 1000; i++ {
			if _, err := os.Stat(filepath.Join(root, "grandchild")); err == nil {
				os.Exit(1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	_ = child.Wait()
	os.Exit(1)
}

func TestNativeCancellationOwnsTunnelTree(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cancel", "deadline", "exit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("FILEES_CANCEL_FIXTURE", root)
			t.Setenv("FILEES_CANCEL_FIXTURE_EXIT", "0")
			if mode == "exit" {
				t.Setenv("FILEES_CANCEL_FIXTURE_EXIT", "1")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			c := &execClient{nativeSVNPath: helper, sshCommand: fmt.Sprintf("\"%s\" -test.run=^TestNativeTunnelFixture$ --", filepath.ToSlash(exe))}
			done := make(chan error, 1)
			go func() {
				_, e := c.nativeCommand(ctx, "", 20*time.Second, "info", "--url", "svn+ssh://fixture.invalid/repo/file")
				done <- e
			}()
			var processes []windows.Handle
			defer func() {
				cancel()
				for _, h := range processes {
					_ = windows.TerminateProcess(h, 99) // exact fixture handles only
					windows.CloseHandle(h)
				}
			}()
			for _, name := range []string{"ssh", "grandchild"} {
				until := time.Now().Add(6 * time.Second)
				for {
					b, e := os.ReadFile(filepath.Join(root, name))
					pid, parseErr := strconv.Atoi(string(b))
					if e == nil && parseErr == nil {
						h, e := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
						if e == nil {
							processes = append(processes, h)
						} else if mode != "exit" || !errors.Is(e, windows.ERROR_INVALID_PARAMETER) {
							t.Fatal(e)
						}
						break
					}
					if time.Now().After(until) {
						t.Fatalf("transport did not start %s", name)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if mode == "deadline" {
				<-ctx.Done()
			}
			started := time.Now()
			if mode == "cancel" {
				cancel()
			}
			select {
			case e := <-done:
				if e == nil || (mode != "exit" && !errors.Is(e, ctx.Err())) {
					t.Fatalf("lost refusal/context: %v", e)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("native call blocked draining inherited pipes")
			}
			for _, h := range processes {
				state, e := windows.WaitForSingleObject(h, 1000)
				if e != nil || state != windows.WAIT_OBJECT_0 {
					t.Fatalf("tunnel descendant survived helper: wait=%d err=%v", state, e)
				}
			}
			t.Logf("%s: returned and tunnel tree exited in %s", mode, time.Since(started))
		})
	}
}
