package client

import (
	"bytes"
	"context"
	"testing"
)

// The helper's stderr arrives in arbitrary chunks. Progress lines, even split
// across writes, become reports; every other byte reaches the diagnostic
// buffer unchanged and in order, because error messages are built from it.
func TestProgressSplitterSeparatesReportsFromDiagnostics(t *testing.T) {
	var reports []CommitProgress
	var diagnostic bytes.Buffer
	splitter := &progressSplitter{report: func(p CommitProgress) { reports = append(reports, p) }, inner: &diagnostic}
	for _, chunk := range []string{
		"filees-progress\tfi", "le\nsvn: warning: W1234",
		"56: something\nfilees-progress\tbytes\t300000\n",
		"filees-progress\tfile\nfilees-progress\tbytes\t200\n", // bytes never go backwards
		"filees-progress\tbogus\n", "trailing without newline",
	} {
		if _, err := splitter.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	splitter.flush()
	if got := diagnostic.String(); got != "svn: warning: W123456: something\ntrailing without newline" {
		t.Fatalf("diagnostic = %q", got)
	}
	want := []CommitProgress{{FilesDone: 1}, {FilesDone: 1, BytesSent: 300000}, {FilesDone: 2, BytesSent: 300000}}
	if len(reports) != len(want) {
		t.Fatalf("reports = %+v, want %+v", reports, want)
	}
	for i := range want {
		if reports[i] != want[i] {
			t.Fatalf("report %d = %+v, want %+v", i, reports[i], want[i])
		}
	}
}

// Only a context that asks for progress carries a reporter.
func TestCommitProgressTravelsInTheContext(t *testing.T) {
	if commitProgressFrom(context.Background()) != nil {
		t.Fatal("a plain context carries a reporter")
	}
	if WithCommitProgress(context.Background(), nil) != context.Background() {
		t.Fatal("a nil reporter changed the context")
	}
	called := false
	ctx := WithCommitProgress(context.Background(), func(CommitProgress) { called = true })
	commitProgressFrom(ctx)(CommitProgress{})
	if !called {
		t.Fatal("the reporter did not travel")
	}
}
