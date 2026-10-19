//go:build native_svn_probe && (windows || linux)

package client

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"filees/internal/svnurl"
)

// Real disposable WC, independent CLI oracle, and a deliberately absent CLI in
// the product adapter. Renaming the repository proves the inspection is local.
func TestNativeConversionInventoryOfflineAndRootProperties(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	for _, scenario := range []string{"clean", "r0", "root-property", "text", "missing", "copied", "switched", "sparse", "ignored"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			repo, wc, seed := filepath.Join(root, "repo"), filepath.Join(root, "wc"), filepath.Join(root, "seed")
			run := func(tool string, args ...string) string {
				t.Helper()
				out, err := exec.Command(nativeFixtureTool(t, tool), args...).CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v %s", tool, args, err, out)
				}
				return string(out)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			run("svnadmin", "create", repo)
			uri := svnurl.File(repo)
			if scenario != "r0" {
				for _, dir := range []string{"docs", "branch"} {
					must(os.MkdirAll(filepath.Join(seed, dir), 0700))
					for n := range 80 {
						must(os.WriteFile(filepath.Join(seed, dir, fmt.Sprintf("f%03d.txt", n)), []byte("published bytes\n"), 0600))
					}
				}
				run("svn", "import", "-q", "-m", "conversion fixture", seed, uri)
			}
			run("svn", "checkout", "-q", uri, wc)
			file := filepath.Join(wc, "docs", "f000.txt")
			switch scenario {
			case "root-property":
				run("svn", "propset", "custom:root", "unsent", wc)
			case "text":
				must(os.WriteFile(file, []byte("unsent edit\n"), 0600))
			case "missing":
				must(os.Remove(file))
			case "copied":
				run("svn", "copy", filepath.Join(wc, "docs"), filepath.Join(wc, "copied"))
			case "switched":
				run("svn", "switch", "--ignore-ancestry", uri+"/branch", filepath.Join(wc, "docs"))
			case "sparse":
				run("svn", "update", "--set-depth", "empty", filepath.Join(wc, "docs"))
			case "ignored":
				run("svn", "propset", "svn:ignore", "*.bak", wc)
				run("svn", "commit", "-q", "-m", "ignore policy", wc)
				must(os.Mkdir(filepath.Join(wc, "private.bak"), 0700))
				must(os.WriteFile(filepath.Join(wc, "private.bak", "keep.txt"), []byte("local only"), 0600))
			}
			before := run("svn", "status", "--xml", "--no-ignore", wc)
			must(os.Rename(repo, repo+"-offline"))
			c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "no-cli")}).(*execClient)
			inventory, err := c.ConversionInventory(t.Context(), wc)
			must(err)
			if scenario == "clean" {
				ordinary, err := c.nativeRun(t.Context(), wc, "status", "--inspect-wc", wc, "--depth", "infinity")
				must(err)
				if ordinary["inventory_version"] != nil {
					t.Fatal("ordinary status paid for conversion inventory")
				}
				for _, row := range ordinary["entries"].([]any) {
					if row.(map[string]any)["revision"] != nil {
						t.Fatal("ordinary status gained extra payload")
					}
				}
				for _, args := range [][]string{
					{"status", "--wc", wc, "--conversion-inventory"},
					{"status", "--inspect-wc", wc, "--depth", "empty", "--conversion-inventory"},
					{"status", "--inspect-wc", wc, "--conversion-inventory", "--", "docs"},
					{"status", "--inspect-wc", wc, "--show-updates", "--conversion-inventory"},
				} {
					if _, err := c.nativeRun(t.Context(), wc, args...); err == nil {
						t.Fatalf("partial or remote inventory accepted: %v", args)
					}
				}
			}
			ready := scenario == "clean" || scenario == "r0" || scenario == "ignored"
			if err := inventory.CheckFullClean(); (err == nil) != ready {
				t.Fatalf("scenario=%s clean=%v, want %v", scenario, err, ready)
			}
			var rootSeen, ignoredSeen bool
			for _, entry := range inventory.Entries {
				if entry.Path == "." {
					rootSeen = true
					if scenario == "root-property" && entry.Props != "modified" {
						t.Fatal("root property change omitted")
					}
					if scenario == "r0" && (entry.Revision == nil || *entry.Revision != 0) {
						t.Fatal("r0 omitted")
					}
				}
				if entry.Path == "private.bak" && entry.Item == "ignored" && !entry.Versioned {
					ignoredSeen = true
				}
			}
			if !rootSeen || (scenario == "ignored" && !ignoredSeen) || (scenario == "clean" && len(inventory.Entries) != 163) {
				t.Fatalf("incomplete inventory: root=%v ignored=%v count=%d", rootSeen, ignoredSeen, len(inventory.Entries))
			}
			after := run("svn", "status", "--xml", "--no-ignore", wc)
			if !reflect.DeepEqual(conversionXMLStatus(t, before), conversionXMLStatus(t, after)) {
				t.Fatalf("inspection changed local SVN status:\nbefore=%s\nafter=%s", before, after)
			}
			if scenario == "ignored" {
				data, err := os.ReadFile(filepath.Join(wc, "private.bak", "keep.txt"))
				must(err)
				if string(data) != "local only" {
					t.Fatal("ignored bytes changed")
				}
			}
		})
	}
}

// SVN's XML attribute order comes from a hash and may change between runs.
// Compare parsed facts including root rather than textual serialization.
func conversionXMLStatus(t *testing.T, raw string) any {
	t.Helper()
	var result struct {
		Entries []struct {
			Path   string `xml:"path,attr"`
			Status struct {
				Item     string `xml:"item,attr"`
				Props    string `xml:"props,attr"`
				Revision string `xml:"revision,attr"`
				Switched string `xml:"switched,attr"`
				Copied   string `xml:"copied,attr"`
			} `xml:"wc-status"`
		} `xml:"target>entry"`
	}
	if err := xml.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	return result
}
