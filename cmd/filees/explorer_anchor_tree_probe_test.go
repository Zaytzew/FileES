//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAnchorRemoteTreeNative(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "f\t8192\told.bin\told.bin\nf\t0\tempty.bin\tempty.bin\n")
	set := func(rel, kind string, size int64, old, next string, remove bool) string {
		t.Helper()
		flag := "0"
		if remove {
			flag = "1"
		}
		p.write(strings.Join([]string{"tree-set", "probe", rel, kind, strconv.FormatInt(size, 10), old, next, flag}, "\t"))
		select {
		case fields := <-p.reply:
			return fields[2]
		case <-time.After(12 * time.Second):
			t.Fatal("tree ACK timeout")
		}
		return ""
	}
	id := func(rel string, size int64) string {
		value, err := anchorRemoteIdentity(rel, 7, size)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if hr := set("old.bin", "f", 12345, "old.bin", id("old.bin", 12345), false); hr != hresultOK {
		t.Fatalf("update: %s", hr)
	}
	state, stateErr := (&anchorManager{helper: p.helper}).call(t.Context(), "", "file-state", "--root", root, "--rel", "old.bin")
	if stateErr != nil || state.Size == nil || *state.Size != 12345 || state.Identity != id("old.bin", 12345) {
		t.Fatalf("updated: %+v", state)
	}
	if hr := set("old.bin", "f", 12345, "old.bin", id("old.bin", 12345), false); hr != hresultOK {
		t.Fatalf("replay: %s", hr)
	}
	if hr := set("old.bin", "f", 12345, "unrelated", "another identity", true); hr == hresultOK {
		t.Fatal("foreign identity deleted")
	}
	wide, _ := windows.UTF16PtrFromString(filepath.Join(root, "old.bin"))
	h, err := windows.CreateFile(wide, windows.FILE_READ_ATTRIBUTES|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	hr := set("old.bin", "f", 12345, id("old.bin", 12345), id("old.bin", 12345), true)
	windows.CloseHandle(h)
	if hr == hresultOK {
		t.Fatal("busy leaf deleted")
	}
	if hr := set("folder", "d", 0, "", id("folder", 0), false); hr != hresultOK {
		t.Fatalf("mkdir: %s", hr)
	}
	if hr := set("folder/new.bin", "f", 99, "", id("folder/new.bin", 99), false); hr != hresultOK {
		t.Fatalf("create: %s", hr)
	}
	if hr := set("empty.bin", "f", 0, "empty.bin", id("empty.bin", 0), true); hr != hresultOK {
		t.Fatalf("empty delete: %s", hr)
	}
	if hr := set("old.bin", "f", 12345, "old.bin", id("old.bin", 12345), true); hr != hresultOK {
		t.Fatalf("delete: %s", hr)
	}
	if _, err := os.Lstat(filepath.Join(root, "old.bin")); !os.IsNotExist(err) {
		t.Fatalf("delete result: %v", err)
	}
	if hr := set("folder/new.bin", "f", 99, id("folder/new.bin", 99), id("folder/new.bin", 99), true); hr != hresultOK {
		t.Fatalf("child delete: %s", hr)
	}
	if hr := set("folder", "d", 0, id("folder", 0), id("folder", 0), true); hr != hresultOK {
		t.Fatalf("directory delete: %s", hr)
	}
	if err := os.WriteFile(filepath.Join(root, "local.bin"), []byte("do not touch"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{false, true} {
		if hr := set("local.bin", "f", 1, "local.bin", id("local.bin", 1), remove); hr == hresultOK {
			t.Fatal("ordinary file accepted")
		}
	}
	if got, err := os.ReadFile(filepath.Join(root, "local.bin")); err != nil || string(got) != "do not touch" {
		t.Fatal("local bytes changed")
	}
	p.accept("local.bin", true)
	if hr := set("local.bin", "f", 1, "filees.local/v1", id("local.bin", 1), true); hr == hresultOK {
		t.Fatal("adopted file deleted")
	}
	select {
	case <-p.fetch:
		t.Fatal("remote metadata mutation fetched bytes")
	default:
	}
}

// Production seed runs before connect. A separate process must not seed an
// already connected provider's root (Windows correctly refuses that access).
func TestAnchorSeedRetryNative(t *testing.T) {
	root := t.TempDir()
	manager := &anchorManager{helper: probeTool(t, "FILEES_CFAPI")}
	probeRun(t, root, manager.helper, "register", "--root", root, "--identity", "retry-fixture")
	t.Cleanup(func() { probeRun(t, root, manager.helper, "unregister", "--root", root) })
	listing := "f\t99\tretry.bin\tretry.bin\n"
	for i := range 2 {
		answer, err := manager.call(t.Context(), listing, "placeholders", "--root", root)
		if err != nil || !answer.OK {
			t.Fatal("seed replay refused own unchanged entry", i, answer, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "local.bin"), []byte("local bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	answer, err := manager.call(t.Context(), "f\t1\tlocal.bin\tlocal.bin\n", "placeholders", "--root", root)
	if err == nil && answer.OK {
		t.Fatal("seed reported partial/obstructed listing as complete")
	}
	if got, err := os.ReadFile(filepath.Join(root, "local.bin")); err != nil || string(got) != "local bytes" {
		t.Fatal("seed changed ordinary local bytes", err)
	}
}
