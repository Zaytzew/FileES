package mobileworker

import (
	"bytes"
	"context"
	"errors"
	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorReconcileLegacyRetry(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	id := uuid.NewString()
	body := packTree(t, map[string][]byte{"note.txt": []byte("restored")})
	p := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body)}
	rec := Record{RequestID: id, ClientID: "c", RepoID: "r", Path: p.ParentPath, PayloadHash: p.Sha256, State: v1.OpStateRejected}
	if err := a.Ledger.Put(rec); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(a.Ledger.path(id))
	if _, err := a.Reconcile(context.Background(), id, false, ""); !errors.Is(err, errOperationUncertain) {
		t.Fatalf("got %v", err)
	}
	unchanged, _ := os.ReadFile(a.Ledger.path(id))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("unconfirmed recovery mutated record")
	}
	lock, err := a.Ledger.lockOperation(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Reconcile(context.Background(), id, true, "maintenance: workers stopped"); err == nil {
		t.Fatal("busy operation changed")
	}
	lock.Close()
	if _, err = a.Reconcile(context.Background(), id, true, ""); err == nil {
		t.Fatal("missing reason accepted")
	}
	got, err := a.Reconcile(context.Background(), id, true, "maintenance: workers stopped")
	if err != nil || !got.RecoveryFenced || got.State != v1.OpStateRejected {
		t.Fatalf("got %+v %v", got, err)
	}
	audits, _ := filepath.Glob(filepath.Join(a.Ledger.Dir, "recovery-"+id+"-*.json"))
	if len(audits) != 1 {
		t.Fatalf("audit %v", audits)
	}
	audit, _ := os.ReadFile(audits[0])
	if !bytes.Contains(audit, []byte("maintenance: workers stopped")) {
		t.Fatal("missing audit reason")
	}
	result, err := a.UploadTree(context.Background(), "c", id, p, bytes.NewReader(body))
	if err != nil || result.Revision < 1 {
		t.Fatalf("retry %+v %v", result, err)
	}
	if string(catRepo(t, repo, "mobile-uploads/note.txt")) != "restored" {
		t.Fatal("missing upload")
	}
}

func TestOperatorReconcileFindsCommitWithoutReplaying(t *testing.T) {
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
	rec.State = v1.OpStateRejected
	rec.RecoveryFenced = false
	rec.BeforeRevision = 0
	rec.Revision = 0
	if err = a.Ledger.Put(*rec); err != nil {
		t.Fatal(err)
	}
	uploadTree(t, a, map[string][]byte{"note.txt": []byte("new desktop")})
	got, err := a.Reconcile(context.Background(), id, true, "maintenance")
	if err != nil || got.Revision != first.Revision || got.State != v1.OpStateCommitted {
		t.Fatalf("got %+v %v", got, err)
	}
	if string(catRepo(t, repo, "mobile-uploads/note.txt")) != "new desktop" {
		t.Fatal("newer content overwritten")
	}
	if _, err = a.Reconcile(context.Background(), "../escape", true, "reason"); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
