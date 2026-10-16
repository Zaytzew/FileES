package storagewatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObservationEpochMutationCrashAndRestart(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	stamp := func(wantClean bool) string {
		t.Helper()
		s, clean, err := g.ObservationStamp()
		if err != nil || clean != wantClean {
			t.Fatalf("stamp clean=%v err=%v", clean, err)
		}
		return s
	}
	legacy := stamp(false)
	a, err := g.beginWriter()
	if err != nil {
		t.Fatal(err)
	}
	dirty := stamp(false)
	if dirty == legacy {
		t.Fatal("admission did not invalidate")
	}
	b, err := g.beginWriter()
	if err != nil {
		t.Fatal(err)
	}
	if stamp(false) == dirty {
		t.Fatal("second writer hidden")
	}
	if err := g.finishWriter(b); err != nil {
		t.Fatal(err)
	}
	if err := g.requireFinishedWriters(); err == nil {
		t.Fatal("other unfinished writer forgotten")
	}
	// A fresh process must see the durable orphan, even after another success.
	restarted := Gate{Root: g.Root}
	if _, clean, err := restarted.ObservationStamp(); err != nil || clean {
		t.Fatal(clean, err)
	}
	if err := g.finishWriter(a); err != nil {
		t.Fatal(err)
	}
	completed := stamp(true)
	if err := g.requireFinishedWriters(); err != nil {
		t.Fatal(err)
	}
	if s, clean, err := restarted.ObservationStamp(); err != nil || !clean || s != completed {
		t.Fatal(s, clean, err)
	}
	c, err := g.beginWriter()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.finishWriter(c); err != nil {
		t.Fatal(err)
	}
	if stamp(true) == completed {
		t.Fatal("whole mutation between observations hidden")
	}
}

func TestObservationEpochFailurePreservesWriter(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	writer, err := g.beginWriter()
	if err != nil {
		t.Fatal(err)
	}
	// Force failure of atomic replacement, without touching any production path.
	if err := os.Mkdir(filepath.Join(g.observationDirectory(), observationEpochName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := g.finishWriter(writer); err == nil {
		t.Fatal("invalid acknowledgement accepted")
	}
	if _, err := os.Stat(writer); err != nil {
		t.Fatal("lost recovery evidence", err)
	}
	if _, _, err := g.ObservationStamp(); err == nil {
		t.Fatal("corrupt epoch trusted")
	}
	if err := g.requireFinishedWriters(); err == nil {
		t.Fatal("corrupt epoch accepted by capacity gate")
	}
}
