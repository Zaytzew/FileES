package client

import (
	"runtime"
	"testing"
)

func TestCommitTransactionRejectsInvalidID(t *testing.T) {
	c := New(Options{}).(*execClient)
	if _, err := c.FindCommit(t.Context(), "file:///unused", "not-an-id", 1); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if _, _, err := c.CommitWithID(t.Context(), "unused", "file:///unused", []string{"a"}, "msg", false, "not-an-id", 1); err == nil {
		t.Fatal("invalid identity accepted")
	}
}

func TestNativeTransactionRejectsDuplicateMarkers(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native public routing is Windows-only; CLI covered by real OpenBSD integration")
	}
	id := "a3ce6e63-d90a-45f9-a921-868d6c0c930e"
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"entries":[{"revision":2,"revprops":{"filees:commit-id":"`+id+`"}},{"revision":3,"revprops":{"filees:commit-id":"`+id+`"}}]}`)
	if _, err := c.FindCommit(t.Context(), "file:///lab", id, 1); err == nil {
		t.Fatal("ambiguous receipt accepted")
	}
}
