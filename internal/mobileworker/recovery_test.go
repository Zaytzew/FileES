package mobileworker

import (
	"bytes"
	"context"
	"errors"
	v1 "filees/pkg/mobile/v1"
	"fmt"
	"github.com/google/uuid"
	"testing"
)

func TestRecoverCommittedTreeNeverReplaysOverNewerContent(t *testing.T) {
	for _, legacyRejected := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy-rejected=", legacyRejected), func(t *testing.T) {
			testRecoverCommittedTree(t, legacyRejected)
		})
	}
}

func testRecoverCommittedTree(t *testing.T, legacyRejected bool) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	body := packTree(t, map[string][]byte{"note.txt": []byte("old")})
	id := uuid.NewString()
	p := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body)}
	first, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := a.Ledger.Lookup(id)
	// Simulate crash after SVN commit but before the final durable ledger write.
	rec.State, rec.Revision, rec.FinalPath = v1.OpStateCommitting, 0, ""
	if legacyRejected {
		rec.State, rec.RecoveryFenced, rec.Operation, rec.BeforeRevision = v1.OpStateRejected, false, "", 0
	}
	if err := a.Ledger.Put(*rec); err != nil {
		t.Fatal(err)
	}
	uploadTree(t, a, map[string][]byte{"note.txt": []byte("new desktop content")})
	// A fresh worker must recover, even though HEAD has advanced.
	restarted := a
	restarted.Reader = SVNReader{}
	got, err := restarted.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != first.Revision {
		t.Fatalf("receipt %+v != %+v", got, first)
	}
	if got := string(catRepo(t, repo, "mobile-uploads/note.txt")); got != "new desktop content" {
		t.Fatalf("overwritten: %q", got)
	}
	// Receipts are bound to caller, destination and metadata, not only hash.
	mutations := []func(*v1.UploadTreePayload){func(p *v1.UploadTreePayload) { p.ParentPath = "mobile-uploads/elsewhere" }, func(p *v1.UploadTreePayload) { p.RepoID = "other" }}
	// Legacy records did not persist count/size; new records bind both.
	if !legacyRejected {
		mutations = append(mutations, func(p *v1.UploadTreePayload) { p.FileCount++ }, func(p *v1.UploadTreePayload) { p.Size++ })
	}
	for _, mutate := range mutations {
		changed := p
		mutate(&changed)
		if _, err := a.UploadTree(context.Background(), "c", id, changed, bytes.NewReader(body)); err == nil {
			t.Fatal("accepted changed intent")
		}
	}
	if _, err := a.UploadTree(context.Background(), "other-client", id, p, bytes.NewReader(body)); err == nil {
		t.Fatal("accepted different client")
	}
	d := Dispatcher{Appender: a, ClientID: "other-client"}
	if s := d.status(context.Background(), id); s.State != v1.OpStateUnknown {
		t.Fatalf("status leaked: %+v", s)
	}
}

func TestUnresolvedIntentDoesNotRunCommitAgain(t *testing.T) {
	requireSVN(t)
	a := newAppender(t, newSeededRepo(t), "rw")
	body := packTree(t, map[string][]byte{"note.txt": []byte("old")})
	id := uuid.NewString()
	p := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body)}
	rec := Record{RequestID: id, ClientID: "c", RepoID: "r", Path: p.ParentPath, PayloadHash: p.Sha256, Operation: v1.OpUploadTree, FileCount: 1, Size: p.Size, State: v1.OpStateCommitting, BeforeRevision: 1}
	if err := a.Ledger.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body)); !errors.Is(err, errOperationUncertain) {
		t.Fatalf("got %v", err)
	}
	lock, err := a.Ledger.lockOperation(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body)); err == nil {
		t.Fatal("concurrent same-ID admitted")
	}
}

func TestRecoverFencedAttemptBeforeCommitAllowsRetry(t *testing.T) {
	requireSVN(t)
	a := newAppender(t, newSeededRepo(t), "rw")
	body := packTree(t, map[string][]byte{"note.txt": []byte("retry")})
	id := uuid.NewString()
	p := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body)}
	rec := Record{RequestID: id, ClientID: "c", RepoID: "r", Path: p.ParentPath, PayloadHash: p.Sha256, Operation: v1.OpUploadTree, FileCount: 1, Size: p.Size, State: v1.OpStateCommitting, BeforeRevision: 1, RecoveryFenced: true}
	if err := a.Ledger.Put(rec); err != nil {
		t.Fatal(err)
	}
	result, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body))
	if err != nil || result.Revision != 2 {
		t.Fatalf("retry %+v %v", result, err)
	}
}

func TestRecoverNoOpTreeDoesNotOverwriteNewContent(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	uploadTree(t, a, map[string][]byte{"note.txt": []byte("same")})
	body := packTree(t, map[string][]byte{"note.txt": []byte("same")})
	id := uuid.NewString()
	p := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body)}
	first, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := a.Ledger.Lookup(id)
	if !rec.NoChanges {
		t.Fatal("not a no-op receipt")
	}
	rec.State = v1.OpStateCommitting
	rec.Revision = 0
	if err := a.Ledger.Put(*rec); err != nil {
		t.Fatal(err)
	}
	uploadTree(t, a, map[string][]byte{"note.txt": []byte("new")})
	d := Dispatcher{Appender: a, ClientID: "c"}
	status := d.status(context.Background(), id)
	if status.State != v1.OpStateCommitted || status.Revision != first.Revision {
		t.Fatalf("recovery %+v", status)
	}
	if string(catRepo(t, repo, "mobile-uploads/note.txt")) != "new" {
		t.Fatal("no-op replayed")
	}
}

func TestLegacyRejectedWithoutAnchorCannotReplay(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	rec := Record{RequestID: uuid.NewString(), ClientID: "c", RepoID: "r", State: v1.OpStateRejected}
	if err := a.Ledger.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err := a.recover(context.Background(), repo, &rec); !errors.Is(err, errOperationUncertain) {
		t.Fatalf("got %v", err)
	}
}
