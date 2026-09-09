//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const repairMarker = "e466476a-6b7b-4e3c-b575-2f4b45c7d865"

func recoveryCall(t *testing.T, f fixture, ok bool, marker, url string, paths ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.probe, "recover-commit", "--disposable-wc", f.wc, "--url", url, "--commit-id", marker, "--revision", "2", "--targets-stdin")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	var got map[string]any
	if json.Unmarshal(out, &got) != nil || got["ok"] != ok || (err == nil) != ok {
		t.Fatalf("recovery ok=%v: %s %v", ok, out, err)
	}
	return got
}

// The copied WC retains pre-postcommit metadata while the original publishes
// the exact receipt. No direct SQLite changes and no simulated C responses.
func repairFixture(t *testing.T, props bool) fixture {
	t.Helper()
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "new.txt"), "published text\n")
	f.svnRun(t, "add", "new.txt")
	if props {
		f.svnRun(t, "propset", "custom:example", "value", "new.txt")
	}
	copyWC := filepath.Join(f.root, "interrupted")
	if err := os.CopyFS(copyWC, os.DirFS(f.wc)); err != nil {
		t.Fatal(err)
	}
	f.svnRun(t, "commit", "-m", "exact receipt", "--with-revprop", "filees:commit-id="+repairMarker, "new.txt")
	f.wc = copyWC
	return f
}

func TestRecoverPlainAddition(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "later-edit"}[later], func(t *testing.T) {
			f := repairFixture(t, false)
			text := "published text\n"
			if later {
				text = "later unsent edit\n"
				write(t, filepath.Join(f.wc, "new.txt"), text)
			}
			for i := 0; i < 2; i++ {
				got := recoveryCall(t, f, true, repairMarker, f.repoURL, "new.txt")
				if got["revision"] != float64(2) || got["reconciled"] != float64(1-i) {
					t.Fatal(got)
				}
				content, err := os.ReadFile(filepath.Join(f.wc, "new.txt"))
				if err != nil || string(content) != text {
					t.Fatal("working text lost", string(content), err)
				}
			}
			status := f.svnRun(t, "status", "--xml", "new.txt")
			expected := "normal"
			if later {
				expected = "modified"
			}
			info := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", "new.txt")
			t.Logf("status=%s native=%v", status, info)
			if later && !bytes.Contains(status, []byte(`item="modified"`)) {
				t.Fatal(string(status))
			}
			if !later && bytes.Contains(status, []byte("<entry")) {
				t.Fatal("not clean", string(status), expected)
			}
			if got := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL))); got != "2" {
				t.Fatal("recovery published again", got)
			}
			// Later BASE must never be downgraded, even if receipt is replayed.
			write(t, filepath.Join(f.wc, "new.txt"), "newer published version\n")
			f.svnRun(t, "commit", "-m", "newer", "new.txt")
			recoveryCall(t, f, true, repairMarker, f.repoURL, "new.txt")
			if got := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", "new.txt"))); got != "3" {
				t.Fatal("downgraded", got)
			}
		})
	}
}

func TestRecoverAdditionRefusalsDoNotMutate(t *testing.T) {
	for _, mode := range []string{"wrong-marker", "wrong-url", "empty", "local-props", "committed-props", "traversal", "all-admission"} {
		t.Run(mode, func(t *testing.T) {
			f := repairFixture(t, mode == "committed-props")
			marker, url, paths := repairMarker, f.repoURL, []string{"new.txt"}
			switch mode {
			case "wrong-marker":
				marker = "c161202e-bfc0-4a09-86cb-a5d2d4a2e4f6"
			case "wrong-url":
				url += "/folder"
			case "empty":
				write(t, filepath.Join(f.wc, "new.txt"), "")
			case "local-props":
				f.svnRun(t, "propset", "custom:later", "value", "new.txt")
			case "committed-props":
				f.svnRun(t, "propdel", "custom:example", "new.txt")
			case "traversal":
				paths = []string{"../invalid"}
			case "all-admission":
				paths = append(paths, "missing.txt")
			}
			before := f.status(t)
			content, err := os.ReadFile(filepath.Join(f.wc, "new.txt"))
			if err != nil {
				t.Fatal(err)
			}
			recoveryCall(t, f, false, marker, url, paths...)
			after := f.status(t)
			actual, err := os.ReadFile(filepath.Join(f.wc, "new.txt"))
			if !bytes.Equal(before, after) || err != nil || !bytes.Equal(content, actual) {
				t.Fatal("refusal mutated WC", string(before), string(after), err)
			}
		})
	}
}
