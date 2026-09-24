package servertool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
)

// svn info reports relative-url as a URL piece. The listing must carry the
// file name, or the interface shows %C4%99 and a release never matches the
// working copy (demo.filees.space, 2026-09-24).
func TestParseLockXMLDecodesTheURLPath(t *testing.T) {
	const xml = `<?xml version="1.0" encoding="UTF-8"?>
<info>
<entry path="uj%C4%99cie 009.jpg" revision="1" kind="file">
<relative-url>^/Zdj%C4%99cia%20z%20budowy/uj%C4%99cie%20009.jpg</relative-url>
<lock><token>opaquelocktoken:1</token><owner>o</owner><comment>c</comment><created>2026-09-24T17:39:00Z</created></lock>
</entry>
</info>
`
	reservations, err := parseLockXML(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 1 || reservations[0].Path != "Zdjęcia z budowy/ujęcie 009.jpg" {
		t.Fatalf("reservations = %+v", reservations)
	}
	broken := strings.Replace(xml, "%C4%99cia", "%C4%9", 1)
	if _, err := parseLockXML(strings.NewReader(broken)); err == nil {
		t.Fatal("a malformed escape was accepted as a path")
	}
}

func TestQueryLiveLocksReturnsFileNamesWithPolishCharacters(t *testing.T) {
	svnadmin, errAdmin := exec.LookPath("svnadmin")
	svn, errSVN := exec.LookPath("svn")
	if errAdmin != nil || errSVN != nil {
		t.Skip("svn tools not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	env := append(os.Environ(), "LC_ALL=", "LC_CTYPE=C.UTF-8")
	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run(root, svnadmin, "create", repo)
	wc := filepath.Join(root, "wc")
	run(root, svn, "checkout", "-q", svnurl.File(repo), wc)
	const name = "Zdjęcia z budowy/ujęcie 009.jpg"
	if err := os.MkdirAll(filepath.Join(wc, "Zdjęcia z budowy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(name)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wc, svn, "add", "-q", "Zdjęcia z budowy")
	run(wc, svn, "commit", "-q", "-m", "init")
	run(wc, svn, "lock", "-q", "-m", "probe", filepath.FromSlash(name))

	reservations, err := queryLiveLocks(context.Background(), svn, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 1 || reservations[0].Path != name {
		t.Fatalf("reservations = %+v", reservations)
	}
}
