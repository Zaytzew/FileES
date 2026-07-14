package client

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
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
	want := []StatusEntry{{Path: "fizyka.docx", Item: "unversioned"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatusXML() = %#v, want %#v", got, want)
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
	want := []StatusEntry{{Path: "ready.txt", Item: "normal"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatusXML() = %#v, want %#v", got, want)
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
