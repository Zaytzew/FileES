package client

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeAddSplitsAtHelperPathLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	root := t.TempDir()
	countFile := filepath.Join(root, "counts")
	binary := filepath.Join(root, "native")
	script := "#!/bin/sh\nn=0\nseen=\nfor a in \"$@\"; do\n  if [ \"$seen\" = 1 ]; then n=$((n+1)); fi\n  if [ \"$a\" = \"--\" ]; then seen=1; fi\ndone\nprintf '%s\\n' \"$n\" >> \"$1\"\nprintf '%s\\n' '{\"schema\":\"filees.native-svn/v1\",\"ok\":true}'\n"
	if err := os.WriteFile(binary, []byte(strings.ReplaceAll(script, "$1", countFile)), 0700); err != nil {
		t.Fatal(err)
	}
	c := New(Options{NativeSVNPath: binary}).(*execClient)
	paths := make([]string, nativePathBatch+1)
	for i := range paths {
		paths[i] = fmt.Sprintf("f%04d.txt", i)
	}
	if _, err := c.nativeAdd(context.Background(), root, paths); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	got := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(got) != 2 || string(got[0]) != fmt.Sprint(nativePathBatch) || string(got[1]) != "1" {
		t.Fatalf("batches: %q", raw)
	}
}
