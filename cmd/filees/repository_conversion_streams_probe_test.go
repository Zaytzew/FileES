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

func TestConversionStreamNativeAcknowledgementRejectsStaleBindingsAndWriters(t *testing.T) {
	root := t.TempDir()
	p := newLocalStateProbe(t, root, "", "f\t8192\tremote.bin\tremote.bin\n")
	m := &anchorManager{helper: p.helper}
	path := filepath.Join(root, "remote.bin")
	if err := os.WriteFile(path+":Zone.Identifier", []byte("zone marker"), 0600); err != nil {
		t.Fatal(err)
	}
	read := func() cfapiAnswer {
		a, e := m.call(t.Context(), "remote.bin\n", "tree-states", "--root", root)
		if e != nil || !a.OK || len(a.States) != 1 {
			t.Fatal(a, e)
		}
		return a.States[0]
	}
	s := read()
	base, info, err := conversionStreamBase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	base.Close()
	id := conversionID(info)
	command := func(usn, binding, identity, size string) string {
		p.write(strings.Join([]string{"stream-set", "probe", "remote.bin", usn, binding, identity, size}, "\t"))
		select {
		case r := <-p.reply:
			if len(r) != 3 || r[0] != "stream-set" || r[1] != "probe" {
				t.Fatal(r)
			}
			return r[2]
		case <-time.After(10 * time.Second):
			t.Fatal("stream metadata ACK timeout")
			return ""
		}
	}
	n, _ := strconv.ParseInt(s.USN, 10, 64)
	for _, args := range [][4]string{{strconv.FormatInt(n+1, 10), id, "remote.bin", "8192"}, {s.USN, "0:1", "remote.bin", "8192"}, {s.USN, id, "other.bin", "8192"}, {s.USN, id, "remote.bin", "8193"}, {"0", id, "remote.bin", "8192"}} {
		if got := command(args[0], args[1], args[2], args[3]); got == hresultOK {
			t.Fatal("stale binding acknowledged", args)
		}
	}
	wide, _ := windows.UTF16PtrFromString(path)
	writer, err := windows.CreateFile(wide, windows.FILE_APPEND_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_OPEN_NO_RECALL, 0)
	if err != nil {
		t.Fatal(err)
	}
	s = read()
	result := command(s.USN, id, "remote.bin", "8192")
	windows.CloseHandle(writer)
	if result == hresultOK {
		t.Fatal("active data writer was not excluded")
	}
	s = read()
	if got := command(s.USN, id, "remote.bin", "8192"); got != hresultOK {
		t.Fatal("valid metadata proof rejected", got)
	}
	after := read()
	if !after.InSync || !after.Placeholder || after.Local || after.OnDisk != 0 || after.Modified != 0 {
		t.Fatal("ack changed primary data", after)
	}
	select {
	case fetch := <-p.fetch:
		t.Fatal("metadata ACK attempted hydration", fetch)
	default:
	}
	if raw, err := os.ReadFile(path + ":Zone.Identifier"); err != nil || string(raw) != "zone marker" {
		t.Fatal("zone marker changed", err)
	}
}

func TestConversionNativeOldStreamHelperRefusesBeforeRename(t *testing.T) {
	old := os.Getenv("FILEES_CFAPI_OLD_STREAM_PROBE")
	if old == "" {
		t.Skip("old CFAPI helper fixture required")
	}
	f := newConversionNativeRunFixture(t)
	f.manager.helper = old
	path := filepath.Join(f.record.LocalPath, "folder", "drawing00.dwg") + ":Zone.Identifier"
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, t.Context())
	r, _ := f.p.local.Get(f.record.OperationID)
	if !strings.Contains(r.LastError, "remote_stream_state_usn_v1") {
		t.Fatal("old helper not refused", r.LastError)
	}
	if _, err := os.Stat(r.PendingLocalPath); !conversionMissing(err) {
		t.Fatal("source moved with old helper")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep" {
		t.Fatal("old helper refusal changed source", err)
	}
}
