package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseStatusXMLReadsWCStatusItemAttribute(t *testing.T) {
	const output = `<?xml version="1.0" encoding="UTF-8"?>
<status>
<target path="fizyka.docx">
<entry path="fizyka.docx">
<wc-status props="none" item="unversioned"></wc-status>
</entry>
</target>
</status>`

	got, err := parseStatusXML(output, "/tmp/wc")
	if err != nil {
		t.Fatalf("parseStatusXML() error = %v", err)
	}
	want := []StatusEntry{{Path: "fizyka.docx", Item: "unversioned", Props: "none"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatusXML() = %#v, want %#v", got, want)
	}
}

func TestSVNXMLOutputDetection(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{args: []string{"status", "--xml", "."}, want: true},
		{args: []string{"log", "--xml", "repository"}, want: true},
		{args: []string{"info", "--show-item", "revision"}, want: false},
	} {
		if got := svnXMLOutput(tc.args); got != tc.want {
			t.Fatalf("svnXMLOutput(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestSVNSSHTransportIsInjectedIntoSVNProcess(t *testing.T) {
	dir := t.TempDir()
	fakeSVN := filepath.Join(dir, "svn")
	if err := os.WriteFile(fakeSVN, []byte("#!/bin/sh\nprintf '%s' \"$SVN_SSH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cli := New(Options{
		SvnPath: fakeSVN, SSHIdentityFile: "/run/filees/id_ed25519",
		SSHKnownHosts: "/run/filees/known_hosts",
	})
	out, err := cli.GetInfo(context.Background(), "svn+ssh://_filees-client@example/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "IdentityAgent=none") || !strings.Contains(out, "UserKnownHostsFile=/run/filees/known_hosts") {
		t.Fatalf("SVN_SSH=%q", out)
	}
}

func TestSVNSSHUserinfoGetsExplicitEmptyPegRevision(t *testing.T) {
	dir := t.TempDir()
	fakeSVN := filepath.Join(dir, "svn")
	if err := os.WriteFile(fakeSVN, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cli := New(Options{SvnPath: fakeSVN, SSHIdentityFile: "/run/filees/id_ed25519", SSHKnownHosts: "/run/filees/known_hosts"})
	out, err := cli.GetInfo(context.Background(), "svn+ssh://_filees-client@example/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "svn+ssh://_filees-client@example/repo@") {
		t.Fatalf("svn args do not escape userinfo as an empty peg revision: %q", out)
	}
}

func TestSVNSSHTransportWithoutIdentityFailsBeforeExec(t *testing.T) {
	cli := New(Options{SvnPath: "/definitely/not/executed"})
	if _, err := cli.GetInfo(context.Background(), "svn+ssh://_filees-client@example/repo"); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("GetInfo error = %v, want missing transport rejection", err)
	}
}

func TestBuildSSHCommandIsPinnedAndNonInteractive(t *testing.T) {
	got := buildSSHCommand("/run/filees/id_ed25519", "/run/filees/known_hosts", 0)
	for _, required := range []string{
		"-F /dev/null", "BatchMode=yes", "IdentitiesOnly=yes",
		"IdentityAgent=none", "PasswordAuthentication=no",
		"KbdInteractiveAuthentication=no", "StrictHostKeyChecking=yes",
		"UserKnownHostsFile=/run/filees/known_hosts",
		"HostKeyAlgorithms=ssh-ed25519", "-i /run/filees/id_ed25519",
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("SSH command %q does not contain %q", got, required)
		}
	}
}

func TestBuildSSHCommandUsesExplicitPort(t *testing.T) {
	got := buildSSHCommand("/run/filees/id_ed25519", "/run/filees/known_hosts", 2223)
	if !strings.Contains(got, "-p 2223") {
		t.Fatalf("SSH command %q does not contain explicit port", got)
	}
}

func TestBuildSSHCommandUsesPinnedConnectionHost(t *testing.T) {
	got := buildSSHCommand("/run/filees/id_ed25519", "/run/filees/known_hosts", 2222, "127.0.0.1:2222")
	if !strings.Contains(got, "HostName=127.0.0.1") || !strings.Contains(got, "HostKeyAlias=[127.0.0.1]:2222") || !strings.Contains(got, "-p 2222") {
		t.Fatalf("SSH command %q does not pin connection endpoint", got)
	}
	if got := buildSSHCommand("/run/filees/id_ed25519", "/run/filees/known_hosts", 22, "bad host"); got != "" {
		t.Fatalf("accepted unsafe connection host: %q", got)
	}
}

func TestBuildSSHCommandRejectsInvalidPort(t *testing.T) {
	if got := buildSSHCommand("/run/filees/id_ed25519", "/run/filees/known_hosts", 65536); got != "" {
		t.Fatalf("accepted invalid port: %q", got)
	}
}

// TestParseLockInfoXML uses `svn status --show-updates --xml`'s real shape
// (confirmed empirically), not `svn info --xml`'s: info only ever reflects
// a lock the querying working copy already knows about itself, which makes
// it blind to a lock taken from a sibling checkout of the same repository -
// exactly the cross-machine case AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md
// depends on (see the doc comment on LockInfo).
func TestParseLockInfoXML(t *testing.T) {
	const output = `<status><target path="/wc/doc.txt"><entry path="/wc/doc.txt"><repos-status item="none" props="none"><lock><token>opaquelocktoken:abc</token><owner>alice</owner><comment>passport</comment><created>2026-07-15T07:39:29.023983Z</created></lock></repos-status></entry></target></status>`
	got, err := parseLockInfoXML(output)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Token != "opaquelocktoken:abc" || got.Owner != "alice" || got.Comment != "passport" {
		t.Fatalf("lock info = %#v", got)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-07-15T07:39:29.023983Z")
	if !got.Created.Equal(want) {
		t.Fatalf("created = %s, want %s", got.Created, want)
	}
}

// TestParseLockInfoXMLFallsBackToWCStatusLock covers the shape observed when
// the querying WC itself already holds the lock and nothing else changed
// server-side to report under repos-status.
func TestParseLockInfoXMLFallsBackToWCStatusLock(t *testing.T) {
	const output = `<status><target path="/wc/doc.txt"><entry path="/wc/doc.txt"><wc-status item="normal" props="normal" revision="1"><lock><token>opaquelocktoken:def</token><owner>root</owner><comment>self</comment><created>2026-07-15T07:39:29.023983Z</created></lock></wc-status></entry></target></status>`
	got, err := parseLockInfoXML(output)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Token != "opaquelocktoken:def" || got.Owner != "root" {
		t.Fatalf("lock info = %#v", got)
	}
}

// TestParseLockInfoXMLWithoutLock covers the shape observed for an
// unmodified, unlocked path: svn status omits the <entry> element entirely
// since there is nothing to report.
func TestParseLockInfoXMLWithoutLock(t *testing.T) {
	got, err := parseLockInfoXML(`<status><target path="/wc/doc.txt"><against revision="1"/></target></status>`)
	if err != nil || got != nil {
		t.Fatalf("lock info = %#v, error = %v", got, err)
	}
}

func TestParseLockListXMLPrefersLiveLockAndKeepsLocalRisk(t *testing.T) {
	const output = `<status><target path="/wc"><entry path="/wc/docs/a.txt"><wc-status item="modified" props="none" revision="1"><lock><token>stale</token><owner>old</owner><created>2026-07-15T07:39:29Z</created></lock></wc-status><repos-status item="none" props="none"><lock><token>live</token><owner>alice</owner><comment>passport</comment><created>2026-07-16T07:39:29Z</created></lock></repos-status></entry><entry path="/wc/docs/b.txt"><wc-status item="normal" props="normal" revision="1"><lock><token>local</token><owner>bob</owner><created>2026-07-17T07:39:29Z</created></lock></wc-status></entry></target></status>`
	rows, err := parseLockListXML(output, "/wc")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%#v", rows)
	}
	if rows[0].Path != filepath.Join("docs", "a.txt") || rows[0].Token != "live" || rows[0].Owner != "alice" || rows[0].LocalItem != "modified" {
		t.Fatalf("first row=%#v", rows[0])
	}
	if rows[1].Path != filepath.Join("docs", "b.txt") || rows[1].Token != "local" || rows[1].LocalItem != "normal" {
		t.Fatalf("second row=%#v", rows[1])
	}
}

func TestParseLockListXMLRejectsTokenlessLock(t *testing.T) {
	_, err := parseLockListXML(`<status><target path="/wc"><entry path="/wc/a"><wc-status item="normal"><lock><owner>alice</owner><created>2026-07-15T07:39:29Z</created></lock></wc-status></entry></target></status>`, "/wc")
	if err == nil || !strings.Contains(err.Error(), "without token") {
		t.Fatalf("err=%v", err)
	}
}

func TestCommitRefusesEmptyPathList(t *testing.T) {
	cli := New(Options{})
	if _, err := cli.Commit(context.Background(), t.TempDir(), nil, "test"); err == nil {
		t.Fatal("Commit() accepted an empty path list")
	}
}

func TestCommitWithRevisionReturnsExactReceiptForMixedRevisionAndDeletion(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if out, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, out)
	}
	repoURL := "file://" + filepath.ToSlash(repository)
	if out, err := exec.Command(svn, "mkdir", "-q", "-m", "init", repoURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn mkdir: %v\n%s", err, out)
	}
	wc := filepath.Join(root, "wc")
	cli := New(Options{SvnPath: svn})
	if _, err := cli.Checkout(context.Background(), repoURL+"/trunk", wc); err != nil {
		t.Fatal(err)
	}
	committer, ok := cli.(interface {
		CommitWithRevision(context.Context, string, string, []string, string, bool) (string, int64, error)
	})
	if !ok {
		t.Fatal("exec client does not expose exact commit receipts")
	}

	path := filepath.Join(wc, "document.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Add(context.Background(), wc, []string{path}); err != nil {
		t.Fatal(err)
	}
	if _, revision, err := committer.CommitWithRevision(context.Background(), wc, repoURL+"/trunk", []string{path}, "add", false); err != nil || revision != 2 {
		t.Fatalf("add receipt revision=%d err=%v", revision, err)
	}
	rootRevision, err := cli.Revision(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	if rootRevision != 1 {
		t.Fatalf("test precondition failed: WC root revision=%d, want mixed root r1", rootRevision)
	}

	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, revision, err := committer.CommitWithRevision(context.Background(), wc, repoURL+"/trunk", []string{path}, "modify", true); err != nil || revision != 3 {
		t.Fatalf("modify receipt revision=%d err=%v", revision, err)
	}
	if _, err := cli.Delete(context.Background(), wc, []string{path}); err != nil {
		t.Fatal(err)
	}
	if _, revision, err := committer.CommitWithRevision(context.Background(), wc, repoURL+"/trunk", []string{path}, "delete", false); err != nil || revision != 4 {
		t.Fatalf("deletion-only receipt revision=%d err=%v", revision, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted path still exists: %v", err)
	}
	if got, err := cli.Revision(context.Background(), repoURL+"/trunk"); err != nil || strconv.FormatInt(got, 10) != "4" {
		t.Fatalf("repository revision=%d err=%v", got, err)
	}
}

func TestCheckoutPreservesExistingDirectoryWithForce(t *testing.T) {
	dir := t.TempDir()
	fakeSVN := filepath.Join(dir, "svn")
	if err := os.WriteFile(fakeSVN, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "existing")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	cli := New(Options{SvnPath: fakeSVN})
	out, err := cli.Checkout(context.Background(), "file:///repository", target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "checkout\n--force\nfile:///repository") {
		t.Fatalf("checkout args = %q", out)
	}
}

func TestParseStatusXMLReadsNormalItemFromVerboseStatus(t *testing.T) {
	const output = `<status><target path="ready.txt"><entry path="ready.txt"><wc-status item="normal" revision="15" props="none"/></entry></target></status>`
	got, err := parseStatusXML(output, "/tmp/wc")
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{{Path: "ready.txt", Item: "normal", Props: "none"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatusXML() = %#v, want %#v", got, want)
	}
}

func TestParsePropGetXMLReturnsRelativePropertyPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "wc")
	input := `<properties><target path="/tmp/wc/a.bin"><property name="svn:needs-lock">*</property></target><target path="/tmp/wc/dir/b.bin"><property name="svn:needs-lock">*</property></target></properties>`
	got, err := parsePropGetXML(input, root)
	if err != nil {
		t.Fatal(err)
	}
	if !got["a.bin"] || !got["dir/b.bin"] || len(got) != 2 {
		t.Fatalf("props=%#v", got)
	}
}

func TestHasMissingPaths(t *testing.T) {
	if !HasMissingPaths([]StatusEntry{{Path: "old.txt", Item: "missing"}}) {
		t.Fatal("missing path was not detected")
	}
	if HasMissingPaths([]StatusEntry{{Path: "new.txt", Item: "unversioned"}, {Path: "edit.txt", Item: "modified"}}) {
		t.Fatal("non-destructive local changes must not block update")
	}
}

func TestRelativizeDoesNotAcceptPrefixSibling(t *testing.T) {
	c := &execClient{}
	root := filepath.Join(string(filepath.Separator), "data", "repo")
	inside := filepath.Join(root, "dir", "file.bin")
	sibling := filepath.Join(string(filepath.Separator), "data", "repo-other", "file.bin")
	got := c.relativize(root, []string{inside, sibling})
	if got[0] != filepath.Join("dir", "file.bin") {
		t.Fatalf("inside path = %q", got[0])
	}
	if got[1] != sibling {
		t.Fatalf("prefix sibling was relativized: %q", got[1])
	}
}

// TestLeadingDashPathsAreTreatedAsPathsNotOptions is the regression test for the
// audit's Finding C (CWE-88 argument injection). A file whose name begins with
// '-' is an ordinary, legal filename that any collaborator can commit to a
// shared repository; once synced, the victim's own daemon feeds it back to the
// svn CLI. Without an explicit "--" end-of-options marker svn parses the name as
// an option instead of a path. This exercises the real svn binary on a real
// working copy — a process mock would not reproduce svn's own argv parsing,
// which is the entire subject of the finding.
func TestLeadingDashPathsAreTreatedAsPathsNotOptions(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if out, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, out)
	}
	repoURL := "file://" + filepath.ToSlash(repository)
	wc := filepath.Join(root, "wc")
	cli := New(Options{SvnPath: svn})
	ctx := context.Background()
	if _, err := cli.Checkout(ctx, repoURL, wc); err != nil {
		t.Fatal(err)
	}

	// Each name is a real svn option spelling, so a missing "--" is not merely
	// mis-parsed but actively consumed as a flag.
	hostile := []string{"--no-ignore", "--depth", "-m"}
	var paths []string
	for _, name := range hostile {
		p := filepath.Join(wc, name)
		if err := os.WriteFile(p, []byte("content of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	if _, err := cli.Add(ctx, wc, paths); err != nil {
		t.Fatalf("Add() on dash-prefixed paths: %v", err)
	}
	entries, err := cli.Status(ctx, wc, paths)
	if err != nil {
		t.Fatalf("Status() on dash-prefixed paths: %v", err)
	}
	scheduled := map[string]bool{}
	for _, e := range entries {
		scheduled[e.Path] = e.Item == "added"
	}
	for _, name := range hostile {
		if !scheduled[name] {
			t.Fatalf("%q was not scheduled for addition; svn consumed it as an option instead of a path (status=%v)", name, entries)
		}
	}

	if _, err := cli.Commit(ctx, wc, paths, "add dash-prefixed names"); err != nil {
		t.Fatalf("Commit() on dash-prefixed paths: %v", err)
	}
	// The commit must have really landed in the repository, not just locally.
	for _, name := range hostile {
		out, err := exec.Command(svn, "cat", "--", repoURL+"/"+name).CombinedOutput()
		if err != nil {
			t.Fatalf("svn cat %q: %v\n%s", name, err, out)
		}
		if got, want := strings.TrimSpace(string(out)), "content of "+name; got != want {
			t.Fatalf("committed content for %q = %q, want %q", name, got, want)
		}
	}

	// Lock/Unlock and PropSet/PropGet take the same trailing-path shape.
	if _, err := cli.Lock(ctx, wc, paths); err != nil {
		t.Fatalf("Lock() on dash-prefixed paths: %v", err)
	}
	if _, err := cli.Unlock(ctx, wc, paths); err != nil {
		t.Fatalf("Unlock() on dash-prefixed paths: %v", err)
	}
	if _, err := cli.PropSet(ctx, wc, "filees:probe", "value", paths); err != nil {
		t.Fatalf("PropSet() on dash-prefixed paths: %v", err)
	}
	got, err := cli.PropGet(ctx, wc, "filees:probe", paths)
	if err != nil {
		t.Fatalf("PropGet() on dash-prefixed paths: %v", err)
	}
	for _, name := range hostile {
		if !strings.Contains(got, name) {
			t.Fatalf("PropGet() output missing %q: %s", name, got)
		}
	}

	// Revert must also address the path rather than a flag.
	if err := os.WriteFile(filepath.Join(wc, hostile[0]), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	reverter, ok := cli.(interface {
		Revert(context.Context, string, []string) (string, error)
	})
	if !ok {
		t.Fatal("exec client does not expose Revert")
	}
	if _, err := reverter.Revert(ctx, wc, paths[:1]); err != nil {
		t.Fatalf("Revert() on a dash-prefixed path: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(wc, hostile[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "content of "+hostile[0] {
		t.Fatalf("Revert() did not restore %q: %q", hostile[0], restored)
	}
}

// TestPathArgsEmitsEndOfOptionsMarker pins the wiring: every path-taking command
// must receive "--" immediately before its first path. This is the unit-level
// half of Finding C's regression coverage; TestSVNRequiresEndOfOptionsMarker
// below pins the premise that makes it necessary.
func TestPathArgsEmitsEndOfOptionsMarker(t *testing.T) {
	c := &execClient{}
	got := c.pathArgs("/wc", []string{"/wc/--no-ignore", "/wc/plain.txt"})
	want := []string{"--", "--no-ignore", "plain.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pathArgs() = %q, want %q", got, want)
	}
	// An empty path list must stay empty: commands like Status rely on "no
	// paths" meaning "the whole working copy", and a bare "--" would not.
	if got := c.pathArgs("/wc", nil); len(got) != 0 {
		t.Fatalf("pathArgs() with no paths = %q, want empty", got)
	}
}

// TestSVNRequiresEndOfOptionsMarker documents, against the real binaries, WHY
// the marker is mandatory: without it svn and svnlook consume a dash-prefixed
// filename as an option. If a future SVN release changed this, the fix's premise
// would no longer hold and this test would say so.
func TestSVNRequiresEndOfOptionsMarker(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn is not installed")
	}
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if out, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, out)
	}
	repoURL := "file://" + filepath.ToSlash(repository)
	wc := filepath.Join(root, "wc")
	if out, err := exec.Command(svn, "checkout", "-q", repoURL, wc).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, out)
	}
	const name = "--no-ignore"
	if err := os.WriteFile(filepath.Join(wc, name), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without the marker: svn must NOT schedule the file, because it read the
	// name as an option. This is the vulnerable argv shape the fix removed.
	add := exec.Command(svn, "add", "--parents", "--depth", "empty", name)
	add.Dir = wc
	if out, err := add.CombinedOutput(); err == nil && strings.Contains(string(out), "A         "+name) {
		t.Fatalf("svn accepted %q as a path without an end-of-options marker; the finding's premise no longer holds: %s", name, out)
	}
	// With the marker it must succeed.
	addOK := exec.Command(svn, "add", "--parents", "--depth", "empty", "--", name)
	addOK.Dir = wc
	if out, err := addOK.CombinedOutput(); err != nil {
		t.Fatalf("svn add with marker: %v\n%s", err, out)
	}
	commit := exec.Command(svn, "commit", "-m", "probe", "--", name)
	commit.Dir = wc
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("svn commit with marker: %v\n%s", err, out)
	}

	// Same for svnlook, which is the server-side half of Finding C.
	if out, err := exec.Command(svnlook, "cat", "-r", "1", repository, name).CombinedOutput(); err == nil {
		t.Fatalf("svnlook accepted %q as a path without a marker: %s", name, out)
	}
	out, err := exec.Command(svnlook, "cat", "-r", "1", "--", repository, name).CombinedOutput()
	if err != nil {
		t.Fatalf("svnlook cat with marker: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "payload" {
		t.Fatalf("svnlook cat with marker returned %q, want %q", out, "payload")
	}
}

// TestBuildSSHCommandKeepsWindowsPathsUsable pins the fix for a defect found
// against a live server: SVN unescapes SVN_SSH before splitting it, so a
// Windows path lost every separator on the way to ssh, which then could not
// open the identity or the pinned known_hosts. Activation had already
// succeeded at that point, so the client looked activated while every
// projection sync failed once a minute.
//
// The assertion is deliberately about what ssh receives, not about the host
// platform: a backslash must never reach SVN_SSH, on any OS.
func TestBuildSSHCommandKeepsWindowsPathsUsable(t *testing.T) {
	// A drive-qualified root on Windows: "\wc" is rooted but drive-relative
	// there, so filepath.IsAbs rejects it and the test would never reach the
	// assertion it exists for.
	root := "/wc"
	if runtime.GOOS == "windows" {
		root = `C:\wc`
	}
	got := buildSSHCommand(filepath.Join(root, "id_ed25519"), filepath.Join(root, "known_hosts"), 2222, "example.net")
	if got == "" {
		t.Fatal("buildSSHCommand rejected an absolute path")
	}
	if strings.Contains(got, `\`) {
		t.Fatalf("SVN_SSH carries a backslash, which Subversion will eat: %q", got)
	}
	for _, want := range []string{"-i " + filepath.ToSlash(filepath.Join(root, "id_ed25519")),
		"UserKnownHostsFile=" + filepath.ToSlash(filepath.Join(root, "known_hosts"))} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSSHCommand = %q, want it to contain %q", got, want)
		}
	}
}

func TestSvnProcessEnvironmentKeepsEnglishMessagesAndUTF8Paths(t *testing.T) {
	got := svnProcessEnvironment([]string{"PATH=/bin", "LC_ALL=C", "LANG=pl_PL.UTF-8"}, "ssh -p 2223")
	want := "LC_ALL=C.UTF-8"
	if runtime.GOOS == "windows" {
		want = "LC_ALL=C"
	}
	locales, leftoverC, ssh := 0, 0, 0
	for _, entry := range got {
		if entry == want {
			locales++
		}
		if entry == "LC_ALL=C" && want != "LC_ALL=C" {
			leftoverC++
		}
		if entry == "SVN_SSH=ssh -p 2223" {
			ssh++
		}
	}
	if locales != 1 || leftoverC != 0 || ssh != 1 {
		t.Fatalf("env=%q locale=%s count=%d leftoverC=%d ssh=%d", got, want, locales, leftoverC, ssh)
	}
}
