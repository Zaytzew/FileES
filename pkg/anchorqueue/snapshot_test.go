package anchorqueue

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

func TestReleaseSnapshotPersistsMembershipAndDoesNotAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: 2, RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32), RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: "folder", Action: "release"}}}
	snapshot := &Snapshot{RootID: "42:123", Paths: []string{"folder/a", "folder/b"}}
	x, err := s.EnqueueSnapshot(p, "operation", "file:///repo", dir, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Paths[0] = "folder/new"
	x.Snapshot.Paths[1] = "folder/new"
	s.List()[0].Snapshot.Paths[0] = "folder/new"
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	x = s.List()[0]
	if strings.Join(x.Snapshot.Paths, ",") != "folder/a,folder/b" || x.Snapshot.RootID != "42:123" {
		t.Fatal(x)
	}
	if _, err := s.EnqueueSnapshot(p, "operation", "file:///repo", dir, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatal("snapshot silently expanded", err)
	}
}

func TestReleaseSnapshotUsesPortableNameModel(t *testing.T) {
	for _, pair := range []struct {
		a, b   string
		reject bool
	}{
		{"ı.txt", "I.txt", true}, {"i.txt", "İ.txt", false}, {"straße.txt", "STRASSE.txt", false},
	} {
		t.Run(pair.a+"-"+pair.b, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(filepath.Join(dir, "queue.json"))
			if err != nil {
				t.Fatal(err)
			}
			p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: 2, RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32), RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "s", RepoID: "r", Path: "folder", Action: "release"}}}
			_, err = s.EnqueueSnapshot(p, "operation", "file:///repo", dir, &Snapshot{RootID: "1:2", Paths: []string{"folder/" + pair.a, "folder/" + pair.b}})
			if (err != nil) != pair.reject {
				t.Fatal("wrong portable collision decision", pair, err)
			}
		})
	}
}

func TestReleaseSnapshotStorageLimitRefusesBeforeAdmission(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 40)
	for i := range paths {
		paths[i] = "folder/" + strings.Repeat("a", 30000) + fmt.Sprint(i)
	}
	p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: int64(len(paths)), RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32), RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "s", RepoID: "r", Path: "folder", Action: "release"}}}
	if _, err := s.EnqueueSnapshot(p, "operation", "file:///repo", dir, &Snapshot{RootID: "1:2", Paths: paths}); !errors.Is(err, ErrFull) || len(s.List()) != 0 || s.Fault() != "" {
		t.Fatal("oversized intention accepted or poisoned", err)
	}
	if fresh, err := Open(filepath.Join(dir, "queue.json")); err != nil || len(fresh.List()) != 0 {
		t.Fatal("oversized intention persisted", err)
	}
	p.Files = 1
	if _, err := s.EnqueueSnapshot(p, "operation", "file:///repo", dir, &Snapshot{RootID: "1:2", Paths: []string{"folder/small"}}); err != nil {
		t.Fatal("size refusal disabled further admission", err)
	}
}

func TestReleaseSnapshotRejectsOutsideDuplicateAndUnboundedMembers(t *testing.T) {
	for _, paths := range [][]string{{"outside"}, {"folder/../escape"}, {"folder/.svn/x"}, {"folder/a", "folder/A"}, {"folder/a:stream"}, {"folder/a", "folder/b"}} {
		dir := t.TempDir()
		s, err := Open(filepath.Join(dir, "queue.json"))
		if err != nil {
			t.Fatal(err)
		}
		p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: 1, RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32), RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "s", RepoID: "r", Path: "folder", Action: "release"}}}
		if _, err := s.EnqueueSnapshot(p, "operation", "file:///repo", dir, &Snapshot{RootID: "1:2", Paths: paths}); err == nil {
			t.Fatal(paths)
		}
	}
}
