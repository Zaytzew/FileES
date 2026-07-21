package client

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestParseLockInfoXML(t *testing.T) {
	const output = `<info><entry><lock><token>opaquelocktoken:abc</token><owner>alice</owner><comment>passport</comment><created>2026-07-15T07:39:29.023983Z</created></lock></entry></info>`
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

func TestParseLockInfoXMLWithoutLock(t *testing.T) {
	got, err := parseLockInfoXML(`<info><entry></entry></info>`)
	if err != nil || got != nil {
		t.Fatalf("lock info = %#v, error = %v", got, err)
	}
}

func TestCommitRefusesEmptyPathList(t *testing.T) {
	cli := New(Options{})
	if _, err := cli.Commit(context.Background(), t.TempDir(), nil, "test"); err == nil {
		t.Fatal("Commit() accepted an empty path list")
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
