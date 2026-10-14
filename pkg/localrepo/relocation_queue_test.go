package localrepo

import (
	"path/filepath"
	"testing"
)

func TestQueuedRelocationReservesBothRootsAndSurvivesRestart(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "lifecycle.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.EnsureConfiguredAttached("server", "a", "svn+ssh://example/a", "rw", filepath.Join(base, "a"), "A")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.EnsureConfiguredAttached("server", "b", "svn+ssh://example/b", "rw", filepath.Join(base, "b"), "B")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	q, err := s.QueueRelocation("server", "a", target)
	if err != nil {
		t.Fatal(err)
	}
	if q.State != StateAttached || !q.RelocationQueued || q.LocalPath != a.LocalPath {
		t.Fatal(q)
	}
	if _, err := s.QueueRelocation("server", "b", filepath.Join(target, "nested")); err == nil {
		t.Fatal("stole pending target")
	}
	if _, err := s.BeginCreate("server", "C", filepath.Join(target, "nested")); err == nil {
		t.Fatal("create stole pending target")
	}
	if _, err := s.QueueRelocation("server", "a", b.LocalPath); err == nil {
		t.Fatal("changed queued target")
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.QueueRelocation("server", "a", target)
	if err != nil || q.OperationID != a.OperationID {
		t.Fatal(q, err)
	}
	changed := q
	changed.PendingLocalPath = filepath.Join(base, "other")
	if _, err := s.StartQueuedRelocation(changed); err == nil {
		t.Fatal("accepted changed target")
	}
	if _, err := s.StartQueuedRelocation(q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRelocation(q.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedRelocationIsSupersededByDetachOrRemoteDeletion(t *testing.T) {
	for _, terminal := range []string{"detach", "remote-delete"} {
		t.Run(terminal, func(t *testing.T) {
			base := t.TempDir()
			s, err := Open(filepath.Join(base, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, _, err := s.EnsureConfiguredAttached("s", "repo", "svn+ssh://example/repo", "rw", filepath.Join(base, "old"), "R")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.QueueRelocation("s", "repo", filepath.Join(base, "new")); err != nil {
				t.Fatal(err)
			}
			if terminal == "detach" {
				_, err = s.BeginDetach("s", "repo", false)
			} else {
				err = s.ObserveRemoteDeletion("s", "repo")
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s.Get(r.OperationID)
			if got.RelocationQueued || got.PendingLocalPath != "" {
				t.Fatal("terminal move remained queued", got)
			}
			if _, err := s.StartQueuedRelocation(r); err == nil {
				t.Fatal("resurrected terminal move")
			}
		})
	}
}
