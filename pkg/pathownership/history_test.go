package pathownership

import (
	"context"
	"testing"
)

func TestObjectContinuityAcrossRenameForkAndLaterDelete(t *testing.T) {
	revisions := []Revision{
		{Number: 1, Author: "creator", Changes: []Change{{Path: "/a.txt", Action: "A", Kind: "file"}}},
		{Number: 2, Author: "administrator", Changes: []Change{{Path: "/a.txt", Action: "D", Kind: "file"}, {Path: "/renamed.txt", Action: "A", Kind: "file", CopyFrom: "/a.txt", CopyRevision: 1}}},
		{Number: 3, Author: "fork-author", Changes: []Change{{Path: "/fork.txt", Action: "A", Kind: "file", CopyFrom: "/renamed.txt", CopyRevision: 2}}},
		{Number: 4, Author: "administrator", Changes: []Change{{Path: "/renamed.txt", Action: "D", Kind: "file"}}},
	}
	first, err := Replay(context.Background(), "repo", 1, revisions[:1])
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := Replay(context.Background(), "repo", 2, revisions[:2])
	if err != nil {
		t.Fatal(err)
	}
	if first.Entries[0].Object != renamed.Entries[0].Object {
		t.Fatal("rename changed object")
	}
	fork, err := Replay(context.Background(), "repo", 3, revisions[:3])
	if err != nil {
		t.Fatal(err)
	}
	after, err := Replay(context.Background(), "repo", 4, revisions)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != 1 || after.Entries[0].FirstCommitter != "fork-author" || after.Entries[0].CreatedRevision != 3 ||
		after.Entries[0].ID == first.Entries[0].ID || after.Entries[0].Object != fork.Entries[0].Object {
		t.Fatalf("fork lost independent history: %+v", after)
	}
}
func TestDirectoryMoveAndReaddedPath(t *testing.T) {
	revisions := []Revision{
		{Number: 1, Author: "a", Changes: []Change{{Path: "/dir", Action: "A", Kind: "dir"}, {Path: "/dir/doc", Action: "A", Kind: "file"}}},
		{Number: 2, Author: "b", Changes: []Change{{Path: "/dir", Action: "D", Kind: "dir"}, {Path: "/moved", Action: "A", Kind: "dir", CopyFrom: "/dir", CopyRevision: 1}}},
		{Number: 3, Author: "c", Changes: []Change{{Path: "/dir", Action: "A", Kind: "dir"}, {Path: "/dir/doc", Action: "A", Kind: "file"}}},
	}
	s, err := Replay(context.Background(), "repo", 3, revisions)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 2 || s.Entries[0].FirstCommitter != "c" || s.Entries[1].FirstCommitter != "a" || s.Entries[0].ID == s.Entries[1].ID {
		t.Fatalf("wrong history: %+v", s)
	}
}
func TestReplayRejectsIncompleteAndAmbiguousHistory(t *testing.T) {
	initial := Revision{Number: 1, Author: "a", Changes: []Change{{Path: "/a", Action: "A", Kind: "file"}}}
	if _, err := Replay(context.Background(), "repo", 2, []Revision{initial}); err == nil {
		t.Fatal("accepted incomplete history")
	}
	fork := Revision{Number: 2, Author: "b", Changes: []Change{{Path: "/a", Action: "D", Kind: "file"}, {Path: "/b", Action: "A", Kind: "file", CopyFrom: "/a", CopyRevision: 1}, {Path: "/c", Action: "A", Kind: "file", CopyFrom: "/a", CopyRevision: 1}}}
	if _, err := Replay(context.Background(), "repo", 2, []Revision{initial, fork}); err == nil {
		t.Fatal("guessed ambiguous successor")
	}
}

func TestCopiedDirectoryChildDeletesAndReplacements(t *testing.T) {
	revisions := []Revision{
		{Number: 1, Author: "creator", Changes: []Change{
			{Path: "/a", Action: "A", Kind: "dir"},
			{Path: "/a/deleted", Action: "A", Kind: "file"},
			{Path: "/a/sub", Action: "A", Kind: "dir"},
			{Path: "/a/sub/old", Action: "A", Kind: "file"},
			{Path: "/a/kept", Action: "A", Kind: "file"},
		}},
		{Number: 2, Author: "forker", Changes: []Change{
			{Path: "/b", Action: "A", Kind: "dir", CopyFrom: "/a", CopyRevision: 1},
			{Path: "/b/deleted", Action: "D", Kind: "file"},
			{Path: "/b/sub", Action: "R", Kind: "dir"},
			{Path: "/b/sub/new", Action: "A", Kind: "file"},
		}},
	}
	s, err := Replay(t.Context(), "repo", 2, revisions)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]Entry{}
	for _, e := range s.Entries {
		paths[e.Path] = e
	}
	if len(paths) != 5 || paths["b/kept"].FirstCommitter != "forker" || paths["b/sub/new"].FirstCommitter != "forker" {
		t.Fatalf("copied subtree: %+v", paths)
	}
	if paths["b/deleted"].ID != "" || paths["b/sub/old"].ID != "" {
		t.Fatal("deleted descendant resurrected")
	}
}
