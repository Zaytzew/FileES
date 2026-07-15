package client

import (
	"context"
	"path/filepath"
	"reflect"
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
	if _, err := cli.Commit(context.Background(), t.TempDir(), nil, "test", "", ""); err == nil {
		t.Fatal("Commit() accepted an empty path list")
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

func TestRedactArgsHidesPasswordWithoutMutatingInput(t *testing.T) {
	in := []string{"--username", "user", "--password", "secret", "status"}
	got := redactArgs(in)
	want := []string{"--username", "user", "--password", "<redacted>", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redactArgs() = %#v, want %#v", got, want)
	}
	if in[3] != "secret" {
		t.Fatalf("redactArgs mutated input: %#v", in)
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
