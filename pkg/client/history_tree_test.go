package client

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func traceLines(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out [][]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line []string
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return out
}

const treePlan = `{"path":"Docs","kind":"dir"}` + "\n" +
	`{"path":"Docs/a.txt","kind":"file","size":5}` + "\n" +
	`{"path":"b:c.txt","kind":"file","size":7}` + "\n"

func TestHistoryListTreeChecksThePlanAgainstTheReceipt(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":4,"dirs":1,"files":2,"bytes":12}`)
	t.Setenv("FILEES_TEST_RA_PLAN", treePlan)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	plan := filepath.Join(t.TempDir(), "plan.ndjson")

	summary, err := c.HistoryListTree(t.Context(), "file:///lab", 4, plan)
	if err != nil || summary != (HistoryTreeSummary{Revision: 4, Dirs: 1, Files: 2, Bytes: 12}) {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if argv := traceLines(t, trace)[0]; !reflect.DeepEqual(argv, []string{"list-tree", "--url", "file:///lab", "--revision", "4", "--out", plan}) {
		t.Fatalf("argv = %v", argv)
	}
	var nodes []HistoryTreeNode
	if err := EachHistoryTreeNode(plan, func(node HistoryTreeNode) error { nodes = append(nodes, node); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []HistoryTreeNode{{Path: "Docs", Kind: "dir", Size: -1}, {Path: "Docs/a.txt", Kind: "file", Size: 5}, {Path: "b:c.txt", Kind: "file", Size: 7}}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("nodes = %+v", nodes)
	}

	receipt := func(rev, bytes string) string {
		return `{"schema":"filees.native-svn/v1","ok":true,"revision":` + rev + `,"dirs":1,"files":2,"bytes":` + bytes + `}`
	}
	for name, tc := range map[string]struct{ reply, plan string }{
		"bytes differ":       {receipt("4", "13"), treePlan},
		"revision differs":   {receipt("3", "12"), treePlan},
		"climbing path":      {receipt("4", "12"), strings.Replace(treePlan, "b:c.txt", "../c.txt", 1)},
		"absolute path":      {receipt("4", "12"), strings.Replace(treePlan, `"Docs"`, `"/Docs"`, 1)},
		"unknown kind":       {receipt("4", "12"), strings.Replace(treePlan, `"kind":"dir"`, `"kind":"link"`, 1)},
		"directory size":     {receipt("4", "12"), strings.Replace(treePlan, `"kind":"dir"`, `"kind":"dir","size":0`, 1)},
		"file without size":  {receipt("4", "12"), strings.Replace(treePlan, `,"size":7`, ``, 1)},
		"negative size":      {receipt("4", "12"), strings.Replace(treePlan, `"size":7`, `"size":-7`, 1)},
		"not JSON":           {receipt("4", "12"), treePlan + "garbage\n"},
		"receipt lacks dirs": {`{"schema":"filees.native-svn/v1","ok":true,"revision":4,"files":2,"bytes":12}`, treePlan},
	} {
		t.Run(name, func(t *testing.T) {
			c := raFake(t, tc.reply)
			t.Setenv("FILEES_TEST_RA_PLAN", tc.plan)
			if summary, err := c.HistoryListTree(t.Context(), "file:///lab", 4, filepath.Join(t.TempDir(), "plan.ndjson")); err == nil {
				t.Fatalf("accepted %s: %+v", name, summary)
			}
		})
	}

	t.Run("refused before the helper runs", func(t *testing.T) {
		c := raFake(t, receipt("4", "12"))
		trace := filepath.Join(t.TempDir(), "trace")
		t.Setenv("FILEES_TEST_RA_TRACE", trace)
		taken := filepath.Join(t.TempDir(), "taken.ndjson")
		if err := os.WriteFile(taken, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.HistoryListTree(t.Context(), "file:///lab", 4, taken); err == nil {
			t.Fatal("existing plan file accepted")
		}
		if _, err := c.HistoryListTree(t.Context(), "file:///lab", 4, "plan.ndjson"); err == nil {
			t.Fatal("relative plan file accepted")
		}
		if _, err := c.HistoryListTree(t.Context(), "file:///lab", -1, filepath.Join(t.TempDir(), "p")); err == nil {
			t.Fatal("negative revision accepted")
		}
		t.Setenv("FILEES_TEST_RA_OLD_HELPER", "1")
		if _, err := c.HistoryListTree(t.Context(), "file:///lab", 4, filepath.Join(t.TempDir(), "p")); err == nil {
			t.Fatal("helper without history_tree_v1 accepted")
		}
		if _, err := os.Stat(trace); !os.IsNotExist(err) {
			t.Fatalf("list-tree ran for a refused request (err=%v)", err)
		}
	})
}

func TestHistoryFetchTreeSendsTheManifestAndBelievesTheDisk(t *testing.T) {
	stage := func(t *testing.T) string {
		t.Helper()
		dest := filepath.Join(t.TempDir(), "stage")
		if err := os.MkdirAll(filepath.Join(dest, "docs(Docs)"), 0700); err != nil {
			t.Fatal(err)
		}
		return dest
	}
	pairs := []HistoryTreePair{{RepoPath: "Docs/deep/text.txt", LocalPath: "docs(Docs)/text.txt"}, {RepoPath: "link", LocalPath: "link"}}
	good := `{"schema":"filees.native-svn/v1","ok":true,"revision":2,"files":[{"path":"docs(Docs)/text.txt","bytes":5}],"skipped":[{"path":"link","reason":"special"}]}`

	c := raFake(t, good)
	t.Setenv("FILEES_TEST_RA_TREE", `{"docs(Docs)/text.txt":"hello"}`)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	dest := stage(t)
	receipt, err := c.HistoryFetchTree(t.Context(), "file:///lab", 2, dest, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt, HistoryTreeReceipt{Files: []HistoryTreeFile{{LocalPath: "docs(Docs)/text.txt", Bytes: 5}}, Skipped: []HistoryTreeSkip{{LocalPath: "link", Reason: "special"}}}) {
		t.Fatalf("receipt = %+v", receipt)
	}
	lines := traceLines(t, trace)
	if !reflect.DeepEqual(lines[0], []string{"fetch-tree", "--url", "file:///lab", "--revision", "2", "--dest", dest, "--manifest-stdin"}) {
		t.Fatalf("argv = %v", lines[0])
	}
	if !reflect.DeepEqual(lines[1], []string{"Docs/deep/text.txt", "docs(Docs)/text.txt", "link", "link", ""}) {
		t.Fatalf("stdin = %q", lines[1])
	}

	for name, tc := range map[string]struct{ reply, tree string }{
		"size differs":         {strings.Replace(good, `"bytes":5`, `"bytes":6`, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"file not on disk":     {good, `{}`},
		"file unaccounted":     {strings.Replace(good, `{"path":"docs(Docs)/text.txt","bytes":5}`, ``, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"unexpected path":      {strings.Replace(good, `"path":"link"`, `"path":"other"`, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"named twice":          {strings.Replace(good, `"path":"link"`, `"path":"docs(Docs)/text.txt"`, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"skipped left on disk": {good, `{"docs(Docs)/text.txt":"hello","link":"link x"}`},
		"unknown skip reason":  {strings.Replace(good, `"special"`, `"too big"`, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"revision differs":     {strings.Replace(good, `"revision":2`, `"revision":1`, 1), `{"docs(Docs)/text.txt":"hello"}`},
		"no skipped list":      {strings.Replace(good, `,"skipped":[{"path":"link","reason":"special"}]`, ``, 1), `{"docs(Docs)/text.txt":"hello"}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := raFake(t, tc.reply)
			t.Setenv("FILEES_TEST_RA_TREE", tc.tree)
			if receipt, err := c.HistoryFetchTree(t.Context(), "file:///lab", 2, stage(t), pairs); err == nil {
				t.Fatalf("accepted %s: %+v", name, receipt)
			}
		})
	}

	t.Run("refused before the helper runs", func(t *testing.T) {
		c := raFake(t, good)
		trace := filepath.Join(t.TempDir(), "trace")
		t.Setenv("FILEES_TEST_RA_TRACE", trace)
		dest := stage(t)
		for name, bad := range map[string][]HistoryTreePair{
			"no pairs":             nil,
			"climbing local":       {{RepoPath: "a", LocalPath: "../a"}},
			"colon in local":       {{RepoPath: "a", LocalPath: "c:a"}},
			"trailing dot local":   {{RepoPath: "a", LocalPath: "a."}},
			"working copy name":    {{RepoPath: "a", LocalPath: ".svn/a"}},
			"absolute repository":  {{RepoPath: "/a", LocalPath: "a"}},
			"climbing repository":  {{RepoPath: "x/../../a", LocalPath: "a"}},
			"local named twice":    {{RepoPath: "a", LocalPath: "a"}, {RepoPath: "b", LocalPath: "a"}},
			"control in repo name": {{RepoPath: "a\nb", LocalPath: "a"}},
		} {
			if _, err := c.HistoryFetchTree(t.Context(), "file:///lab", 2, dest, bad); err == nil {
				t.Fatalf("accepted %s", name)
			}
		}
		if _, err := c.HistoryFetchTree(t.Context(), "file:///lab", 2, "stage", pairs); err == nil {
			t.Fatal("relative destination accepted")
		}
		if _, err := os.Stat(trace); !os.IsNotExist(err) {
			t.Fatalf("fetch-tree ran for a refused request (err=%v)", err)
		}
	})
}
