package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func publicationScanner(t *testing.T, wc string) *Scanner {
	t.Helper()
	s, err := NewScanner(Options{WC: wc, StatePath: filepath.Join(wc, ".filees", "state", "manifest.json"), UseMD5: true, DeletedDebounce: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func publicationScan(t *testing.T, s *Scanner) []Event {
	t.Helper()
	ch := make(chan Event, 100)
	s.scanCycle(context.Background(), ch)
	close(ch)
	var result []Event
	for e := range ch {
		result = append(result, e)
	}
	return result
}
func TestPublicationRecoveryUsesObservedGeneration(t *testing.T) {
	wc := t.TempDir()
	old := filepath.Join(wc, "old.txt")
	dst := filepath.Join(wc, "new.txt")
	if err := os.WriteFile(old, []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	if err := os.Rename(old, dst); err != nil {
		t.Fatal(err)
	}
	// This tests receipt generations, not the Windows-only file identity
	// implementation. A content-preserving move is provable on OpenBSD too.
	events := publicationScan(t, s)
	if len(events) != 1 || events[0].Op != Renamed {
		t.Fatalf("rename fixture: %+v", events)
	}
	p, err := s.CapturePublication([]string{"old.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// Process dies before acknowledgement. Later edits and unrelated additions
	// must not be swallowed by rebuilding a baseline from current disk files.
	if err = os.WriteFile(dst, []byte("later edit after crash, deliberately different length"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(wc, "unrelated.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	r := publicationScanner(t, wc)
	if err = r.AcknowledgePublication(p); err != nil {
		t.Fatal(err)
	}
	// Kill once more immediately after persisted ack; reopening must agree.
	r = publicationScanner(t, wc)
	got := publicationScan(t, r)
	found := map[string]OpType{}
	for _, e := range got {
		found[e.Rel] = e.Op
	}
	if len(found) != 2 || found["new.txt"] != Modified || found["unrelated.txt"] != Added {
		t.Fatalf("lost edit or replayed move: %+v", got)
	}
	if more := publicationScan(t, r); len(more) != 0 {
		t.Fatalf("repeated events: %+v", more)
	}
}

func TestPublicationAckFencesOnlyIncludedEvents(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	if err := os.WriteFile(filepath.Join(wc, "a"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	e := publicationScan(t, s)[0]
	p, err := s.CapturePublication([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(wc, "a"), []byte("newer-content"), 0600); err != nil {
		t.Fatal(err)
	}
	newer := publicationScan(t, s)[0]
	if err = s.AcknowledgePublication(p); err != nil {
		t.Fatal(err)
	}
	if !s.EventAcknowledged(e) || s.EventAcknowledged(newer) {
		t.Fatal("generation fence lost")
	}
	unrelated := e
	unrelated.Rel = "other"
	if s.EventAcknowledged(unrelated) {
		t.Fatal("unselected event swallowed")
	}
	if got := publicationScan(t, s); len(got) != 0 {
		t.Fatalf("ack rolled newer scan backwards: %+v", got)
	}
}

func TestPublicationWriteFailureAndPendingGate(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	s.publicationPending = func() bool { return true }
	if err := os.WriteFile(filepath.Join(wc, "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	publicationScan(t, s)
	if len(s.cur) != 0 {
		t.Fatal("unresolved receipt was silently baselined")
	}
	s.publicationPending = nil
	publicationScan(t, s)
	p, err := s.CapturePublication([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	s.statePath = wc // cannot replace a directory with a manifest
	if err = s.AcknowledgePublication(p); err == nil {
		t.Fatal("write failure hidden")
	}
	if len(s.acknowledged) != 0 {
		t.Fatal("events acknowledged before persistence")
	}
}

func TestPublicationSnapshotDoesNotWaitForEventConsumer(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	for _, p := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(wc, p), []byte(p), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ch := make(chan Event) // no reader, matching a committer busy acknowledging
	done := make(chan struct{})
	go func() { s.scanCycle(context.Background(), ch); close(ch); close(done) }()
	defer func() {
		for range ch {
		}
		<-done
	}()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		_, ready := s.cur["b"]
		s.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("manifest not swapped before delivering events")
		}
		time.Sleep(time.Millisecond)
	}
	captured := make(chan error, 1)
	go func() { _, err := s.CapturePublication([]string{"a", "b"}); captured <- err }()
	select {
	case err := <-captured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full channel deadlocked publication snapshot")
	}
}

func TestPublicationPersistsDirectoryKind(t *testing.T) {
	wc := t.TempDir()
	s := publicationScanner(t, wc)
	publicationScan(t, s)
	if err := os.Mkdir(filepath.Join(wc, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	publicationScan(t, s)
	p, err := s.CapturePublication([]string{"folder"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePublication(p); err != nil {
		t.Fatal(err)
	}
	reopened := publicationScanner(t, wc)
	if !reopened.cur["folder"].IsDir {
		t.Fatal("directory receipt reopened as file")
	}
}
