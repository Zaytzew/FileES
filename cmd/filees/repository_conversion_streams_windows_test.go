//go:build windows && !nocfapi

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConversionStreamInventoryRejectsMalformedRecords(t *testing.T) {
	makeRow := func(name string) []byte {
		w, _ := windows.UTF16FromString(name)
		w = w[:len(w)-1]
		b := conversionAlignedBuffer(24 + 2*len(w))
		binary.LittleEndian.PutUint32(b[4:8], uint32(2*len(w)))
		for i, c := range w {
			binary.LittleEndian.PutUint16(b[24+2*i:], c)
		}
		return b
	}
	for _, name := range []string{":notes:$DATA", ":Zażółć:$DATA", "::$DATA"} {
		if _, err := conversionParseStreams(makeRow(name)); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, name := range []string{":a/b:$DATA", ":a\\b:$DATA", ":notes:$INDEX_ALLOCATION", ":x:y:$DATA"} {
		if _, err := conversionParseStreams(makeRow(name)); err == nil {
			t.Fatal("unsafe stream accepted", name)
		}
	}
	for _, size := range []int{0, 1, 23} {
		if _, err := conversionParseStreams(make([]byte, size)); err == nil {
			t.Fatal("short observation accepted")
		}
	}
	for _, offset := range []uint32{1, 24, 1 << 30} {
		raw := makeRow(":notes:$DATA")
		binary.LittleEndian.PutUint32(raw, offset)
		if _, err := conversionParseStreams(raw); err == nil {
			t.Fatal("bad chain accepted")
		}
	}
	raw := makeRow(":notes:$DATA")
	binary.LittleEndian.PutUint32(raw[4:], 999)
	if _, err := conversionParseStreams(raw); err == nil {
		t.Fatal("unbounded stream name accepted")
	}
	for _, raw := range []string{`[]`, `[{"Name":":notes:$DATA","Size":0,"SHA256":"bad"}]`} {
		if _, err := conversionDecodeStreams(raw); err == nil {
			t.Fatal("bad manifest accepted")
		}
	}
}

func TestConversionStreamPinsPreventOverwriteAndDetectAdditions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("main"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+":Zone.Identifier", []byte("zone"), 0600); err != nil {
		t.Fatal(err)
	}
	base, _, err := conversionOpen(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	manifest, pins, err := conversionPinStreams(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer conversionCloseStreams(pins)
	if !strings.Contains(manifest, "Zone.Identifier") {
		t.Fatal(manifest)
	}
	name, _ := windows.UTF16PtrFromString(path + ":Zone.Identifier")
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		windows.CloseHandle(h)
		t.Fatal("stream writer entered custody")
	}
	name, _ = windows.UTF16PtrFromString(path + ":late")
	h, err = windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
	if err := conversionCheckStreamNames(base, manifest); err == nil {
		t.Fatal("late ADS ignored")
	}
}

func TestConversionStreamDeleteBarrierBlocksNewOpensAndCanBeRevoked(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	base, _, err := conversionOpen(path, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	remove := byte(1)
	if err = windows.SetFileInformationByHandle(windows.Handle(base.Fd()), windows.FileDispositionInfo, &remove, 1); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(path + ":late")
	h, openErr := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_NEW, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if openErr == nil {
		windows.CloseHandle(h)
	}
	observationErr := conversionCheckStreamNames(base, "")
	remove = 0
	err = windows.SetFileInformationByHandle(windows.Handle(base.Fd()), windows.FileDispositionInfo, &remove, 1)
	if openErr == nil || observationErr != nil || err != nil {
		t.Fatal("delete barrier unsafe", openErr, observationErr, err)
	}
	base.Close()
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep" {
		t.Fatal("revocation lost source", err)
	}
}

func TestConversionStreamTargetPinsPreventBaseRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+":notes", []byte("metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	base, _, err := conversionStreamBase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	_, pins, err := conversionPinStreams(t.Context(), base, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conversionCloseStreams(pins)
	wide, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(wide, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		windows.CloseHandle(h)
		t.Fatal("target with preserved ADS could be removed during retirement")
	}
}
