//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/pkg/cloudfiles"
)

// Go starts Windows processes with UTF-16 arguments. The helper must not
// reinterpret the CRT's ANSI argv as UTF-8: stdin is UTF-8 independently.
func TestAnchorUnicodeArguments(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	for _, name := range []string{"ascii-root", "Zażółć-東京-🧪"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			m := &anchorManager{helper: helper}
			call := func(stdin string, args ...string) cfapiAnswer {
				t.Helper()
				answer, err := m.call(t.Context(), stdin, args...)
				if err != nil || !answer.OK {
					t.Fatalf("%v: %+v %v", args, answer, err)
				}
				return answer
			}
			identity := "test\x1frepo"
			if name != "ascii-root" {
				identity = "Zażółć\x1f東京-🧪"
			}
			call("", "register", "--root", root, "--identity", identity)
			t.Cleanup(func() {
				if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
					t.Errorf("unregister: %s (%v)", out, err)
				}
			})
			if !call("", "info", "--root", root).Ours {
				t.Fatal("Unicode registration not recognized")
			}
			parent := "01_PROJEKT-WNĘTRZ-東京-🧪"
			listing := "d\t0\t" + parent + "\t" + parent + "\n"
			call(listing, "placeholders", "--root", root)
			// Resume the actual failure shape: root directory already exists,
			// but the child's Unicode parent must pass through argv.
			call(listing, "placeholders", "--root", root)
			rel := parent + "/Zażółć-東京-🧪.dwg"
			child := "f\t8192\t" + rel + "\tZażółć-東京-🧪.dwg\n"
			call(child, "placeholders", "--root", root, "--rel", parent)
			call(child, "placeholders", "--root", root, "--rel", parent)
			state := call("", "file-state", "--root", root, "--rel", rel)
			if state.Identity != rel || !state.Partial || state.Size == nil || *state.Size != 8192 || !cloudfiles.IsPlaceholder(filepath.Join(root, filepath.FromSlash(rel))) {
				t.Fatalf("wrong Unicode placeholder metadata: %+v", state)
			}
			// Repeating seed must still refuse ordinary local data.
			local := filepath.Join(root, parent, "lokalny-東京.txt")
			if err := os.WriteFile(local, []byte("local work"), 0600); err != nil {
				t.Fatal(err)
			}
			answer, err := m.call(t.Context(), "f\t99\t"+parent+"/lokalny-東京.txt\tlokalny-東京.txt\n", "placeholders", "--root", root, "--rel", parent)
			if err != nil || answer.OK || answer.Error != "create_placeholders" {
				t.Fatalf("local obstruction not refused by helper: %+v %v", answer, err)
			}
			if data, err := os.ReadFile(local); err != nil || string(data) != "local work" {
				t.Fatalf("local work changed: %q %v", data, err)
			}
			answer, err = m.call(t.Context(), child, "placeholders", "--root", root, "--rel", "../東京")
			if answer.OK || answer.Error != "relative_invalid" {
				t.Fatalf("Unicode change weakened traversal refusal: %+v %v", answer, err)
			}
		})
	}
}
