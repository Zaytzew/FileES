package client

import (
	"context"
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
