package watcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanFileMutationFencesOnlySelectedPriorEvents(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	for _, rel := range []string{"file", "other"} {
		if err := os.WriteFile(filepath.Join(wc, rel), []byte("seed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	events := publicationScan(t, s)
	if len(events) != 2 {
		t.Fatal(events)
	}
	if err := s.WithCleanFileMutation(t.Context(), "file", func() error {
		if s.scanMu.TryLock() {
			s.scanMu.Unlock()
			t.Fatal("scanner can see transient filesystem gap")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if s.EventAcknowledged(ev) != (ev.Rel == "file") {
			t.Fatal("wrong queued event retired", ev)
		}
	}
	if err := os.WriteFile(filepath.Join(wc, "file"), []byte("later real edit"), 0600); err != nil {
		t.Fatal(err)
	}
	later := publicationScan(t, s)
	if len(later) != 1 || later[0].Op != Modified || s.EventAcknowledged(later[0]) {
		t.Fatal("later edit lost", later)
	}
}

func TestFileMutationAlsoFencesBackgroundHashing(t *testing.T) {
	s := publicationScanner(t, t.TempDir())
	s.backlog = []backlogItem{{Rel: "missing", Size: 10, Mtime: 1}}
	s.publicationPending = func() bool { return true }
	s.processOneBacklogItem(t.Context())
	if len(s.backlog) != 1 {
		t.Fatal("pending recovery consumed background work")
	}
	s.publicationPending = nil
	started, done := make(chan struct{}), make(chan struct{})
	if err := s.WithCleanFileMutation(t.Context(), "file", func() error {
		go func() { close(started); s.processOneBacklogItem(t.Context()); close(done) }()
		<-started
		select {
		case <-done:
			t.Fatal("background reader entered transient filesystem gap")
		case <-time.After(20 * time.Millisecond):
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background reader did not resume")
	}
	if len(s.backlog) != 0 {
		t.Fatal("unfenced background work did not proceed")
	}
}

func TestFailedCleanFileMutationDoesNotAcknowledge(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	if err := os.WriteFile(filepath.Join(wc, "file"), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	ev := publicationScan(t, s)[0]
	want := errors.New("uncertain provider effect")
	if err := s.WithCleanFileMutation(t.Context(), "file", func() error { return want }); !errors.Is(err, want) || s.EventAcknowledged(ev) {
		t.Fatal("uncertain mutation acknowledged", err)
	}
	if !s.scanMu.TryLock() {
		t.Fatal("scanner fence leaked")
	}
	s.scanMu.Unlock()
}
