package client

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNativeConflictMetadataRequiresEvidence(t *testing.T) {
	for _, value := range []map[string]any{
		{},
		{"entries": []any{}},
		{"entries": []any{map[string]any{"path": "file"}}}, // older helper
		{"entries": []any{map[string]any{"path": "file", "conflicts": nil}}},
		{"entries": []any{map[string]any{"path": "other", "conflicts": []any{}}}},
		{"entries": []any{map[string]any{"path": "file", "conflicts": []any{42}}}},
	} {
		if _, err := parseNativeConflicts(value, "file"); err == nil {
			t.Fatalf("accepted %+v", value)
		}
	}
	details, err := parseNativeConflicts(map[string]any{"entries": []any{map[string]any{"path": "file", "conflicts": []any{}}}}, "file")
	if err != nil || len(details) != 0 {
		t.Fatalf("explicit no conflict: %+v %v", details, err)
	}
}

// Compare the CLI and C observations directly, including on OpenBSD where
// the desktop adapter intentionally does not select native WC operations.
func TestConflictMetadataRealSVN(t *testing.T) {
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " absent")
		}
	}
	run := func(bin string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(t.Context(), bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", bin, args, err, out)
		}
	}
	root := t.TempDir()
	repo, author, reader := filepath.Join(root, "repo"), filepath.Join(root, "author"), filepath.Join(root, "reader")
	p := filepath.ToSlash(repo)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	repoURL := (&url.URL{Scheme: "file", Path: p}).String()
	run("svnadmin", "create", repo)
	run("svn", "checkout", repoURL, author)
	if err := os.Mkdir(filepath.Join(author, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rel := "docs/plan.txt"
	write(filepath.Join(author, rel), "base\n")
	run("svn", "add", filepath.Join(author, "docs"))
	run("svn", "commit", "-m", "base", author)
	run("svn", "checkout", repoURL, reader)
	write(filepath.Join(reader, rel), "local\n")
	for _, suffix := range []string{".mine", ".r1", ".r2"} {
		write(filepath.Join(reader, rel+suffix), "ordinary")
	}
	write(filepath.Join(author, rel), "server\n")
	run("svn", "commit", "-m", "server", author)
	run("svn", "update", "--accept", "postpone", reader)
	c := New(Options{Timeout: 20 * time.Second}).(*execClient)
	want, err := c.ConflictDetails(t.Context(), reader, rel)
	if err != nil || len(want) != 1 || want[0].Type != "text" || want[0].Mine == "" || want[0].Mine == rel+".mine" {
		t.Fatalf("CLI metadata: %+v %v", want, err)
	}
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Log("CLI checked; native metadata comparison needs FILEES_SVN_PROBE")
		return
	}
	c.nativeSVNPath = helper
	raw, err := c.nativeRun(t.Context(), reader, "info", "--inspect-wc", reader, "--", rel)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseNativeConflicts(raw, rel)
	if err != nil {
		t.Fatal(err)
	}
	got, err = normalizeConflictDetails(reader, rel, got)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native %+v != CLI %+v: %v", got, want, err)
	}
}

func TestConflictMetadataPaths(t *testing.T) {
	wc := t.TempDir()
	for _, path := range []string{"relative.mine", filepath.Join(wc, "..", "outside"), filepath.Join(wc, "other", "file.mine"), filepath.Join(wc, "docs", "file")} {
		if _, err := normalizeConflictDetails(wc, "docs/file", []ConflictDetail{{Type: "text", Mine: path}}); err == nil {
			t.Fatalf("accepted unsafe artifact %s", path)
		}
	}
	details, err := normalizeConflictDetails(wc, "docs/file", []ConflictDetail{{Type: "text", Base: filepath.Join(wc, "docs", "file.2.r1"), Mine: filepath.Join(wc, "docs", "file.3.mine"), Theirs: filepath.Join(wc, "docs", "file.4.r2")}})
	if err != nil || details[0].Mine != "docs/file.3.mine" || details[0].Base != "docs/file.2.r1" || details[0].Theirs != "docs/file.4.r2" {
		t.Fatalf("metadata %+v %v", details, err)
	}
}
