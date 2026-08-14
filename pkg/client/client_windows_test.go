//go:build windows

package client

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSVNErrorDecodesActiveWindowsCodePage(t *testing.T) {
	svn, err := exec.LookPath("svn.exe")
	if err != nil {
		t.Skipf("svn.exe unavailable: %v", err)
	}
	target := filepath.Join(t.TempDir(), "nie-istnieje-ŁÓDŹ-ąęź")
	_, err = New(Options{SvnPath: svn}).GetInfo(context.Background(), target)
	if err == nil {
		t.Fatal("svn info unexpectedly accepted a missing path")
	}
	got := err.Error()
	if !utf8.ValidString(got) || !strings.Contains(got, "nie-istnieje-ŁÓDŹ-ąęź") {
		t.Fatalf("decoded svn diagnostic = %q", got)
	}
}
