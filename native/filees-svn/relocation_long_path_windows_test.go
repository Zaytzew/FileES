//go:build windows && native_svn_probe

package nativesvnprobe

import (
	"encoding/json"
	"filees/pkg/client"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMovedWorkingCopyBeyondMaxPathRemainsInspectable(t *testing.T) {
	f := newFixture(t, "draft.txt")
	target := filepath.Join(f.root, strings.Repeat("folder", 16), strings.Repeat("deep", 20), strings.Repeat("long", 20), "moved")
	if len(target) < 270 {
		t.Fatal("fixture path is not long")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.wc, target); err != nil {
		t.Fatal(err)
	}
	f.wc = target
	c := client.New(client.Options{NativeSVNPath: f.probe, SvnPath: filepath.Join(f.root, "no-cli")})
	if info, err := c.GetInfo(t.Context(), target); err != nil || !strings.Contains(info, f.repoURL) {
		t.Fatalf("product client info=%q err=%v", info, err)
	}
	// The helper gets an absolute WC argument. Its process directory must not
	// itself exceed CreateProcessW's current-directory limit.
	for _, args := range [][]string{{"info", "--inspect-wc", target}, {"status", "--disposable-wc", target, "--", "draft.txt"}} {
		out, err := execute(t, f.root, f.probe, args...)
		var got map[string]any
		if jsonErr := json.Unmarshal(out, &got); jsonErr != nil || err != nil || got["ok"] != true {
			t.Fatalf("native long-path call %s: err=%v json=%v output=%s", args[0], err, jsonErr, out)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(target, "draft.txt")); err != nil || string(raw) != "first version\n" {
		t.Fatalf("bytes=%q err=%v", raw, err)
	}
}
