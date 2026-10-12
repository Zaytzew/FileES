package localrepo

import (
	"path/filepath"
	"testing"
)

func TestExplorerShortcutIsLocalDurableAndSurvivesMove(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "lifecycle.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	attach := func(server, repo string, anchor bool) Record {
		t.Helper()
		var r Record
		var e error
		if anchor {
			r, e = s.BeginAnchorAttach(server, repo, filepath.Join(base, server+repo), false)
		} else {
			r, e = s.BeginAttach(server, repo, filepath.Join(base, server+repo), false)
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.ApproveAttach(r.OperationID, server, repo, "svn+ssh://example/"+repo, "rw"); e != nil {
			t.Fatal(e)
		}
		r, e = s.MarkAttached(r.OperationID, repo)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	a, b, ordinary := attach("one", "repo", true), attach("two", "repo", true), attach("one", "ordinary", false)
	if a.ExplorerShortcut || b.ExplorerShortcut {
		t.Fatal("implicit shortcut")
	}
	if _, err = s.SetExplorerShortcut(ordinary.OperationID, true); err == nil {
		t.Fatal("ordinary folder accepted")
	}
	if _, err = s.SetExplorerShortcut(a.OperationID, true); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = s.Get(a.OperationID)
	b, _ = s.Get(b.OperationID)
	if !a.ExplorerShortcut || b.ExplorerShortcut {
		t.Fatal("scope/restart preference lost")
	}
	move, err := s.BeginRelocation("one", "repo", filepath.Join(base, "renamed"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetExplorerShortcut(move.OperationID, false); err == nil {
		t.Fatal("pending move accepted")
	}
	a, err = s.CompleteRelocation(move.OperationID)
	if err != nil || !a.ExplorerShortcut {
		t.Fatal("move lost setting", err)
	}
	if _, err = s.SetExplorerShortcut(a.OperationID, false); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = s.Get(a.OperationID)
	if a.ExplorerShortcut {
		t.Fatal("hide not durable")
	}
}
