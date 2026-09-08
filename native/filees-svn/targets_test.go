//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommitTargetsStdin(t *testing.T) {
	f := newFixture(t, "old.txt")
	paths := make([]string, 1200)
	var input bytes.Buffer
	for i := range paths {
		paths[i] = fmt.Sprintf("%04d_新_%s.txt", i, strings.Repeat("segment", 5))
		write(t, filepath.Join(f.wc, paths[i]), "synthetic\n")
		input.WriteString(paths[i])
		input.WriteByte(0)
	}
	for i := 0; i < len(paths); i += 400 {
		f.jsonCall(t, true, append([]string{"add", "--disposable-wc", f.wc, "--"}, paths[i:i+400]...)...)
	}
	write(t, filepath.Join(f.wc, "unselected.txt"), "not in transaction\n")
	f.jsonCall(t, true, "add", "--disposable-wc", f.wc, "--", "unselected.txt")
	call := func(input []byte, ok bool, extra ...string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		args := append([]string{"commit", "--disposable-wc", f.wc, "-m", "stdin targets", "--targets-stdin"}, extra...)
		cmd := exec.CommandContext(ctx, f.probe, args...)
		cmd.Env = os.Environ()
		if runtime.GOOS != "windows" {
			cmd.Env = append(cmd.Env, "LC_ALL=C.UTF-8")
		}
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		var result map[string]any
		if json.Unmarshal(out, &result) != nil || result["schema"] != "filees.native-svn/v1" || result["ok"] != ok || (err == nil) != ok {
			t.Fatalf("ok=%v output=%s error=%v", ok, out, err)
		}
		return result
	}
	var tooMany bytes.Buffer
	for i := 0; i < 65537; i++ {
		fmt.Fprintf(&tooMany, "%d\x00", i)
	}
	for _, bad := range [][]byte{nil, []byte("old.txt"), []byte("old.txt\x00../escape\x00"), []byte("old.txt\x00old.txt\x00"), []byte(".filees/state\x00"), {0xf4, 0x90, 0x80, 0x80, 0}, {0xe2, 0}, tooMany.Bytes(), bytes.Repeat([]byte{'a'}, 16*1024*1024+1)} {
		call(bad, false)
	}
	call(input.Bytes(), false, "--", "old.txt")
	if got := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL))); got != "1" {
		t.Fatalf("refusal changed HEAD: %s", got)
	}
	got := call(input.Bytes(), true)
	if got["revision"] != float64(2) {
		t.Fatal(got)
	}
	log := f.svnRun(t, "log", "--xml", "-v", "-r", "2", f.repoURL)
	if bytes.Count(log, []byte("</path>")) != len(paths) || bytes.Contains(log, []byte("unselected")) {
		t.Fatalf("wrong commit scope: %s", log)
	}
	if got := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL))); got != "2" {
		t.Fatal("transaction split", got)
	}
}
