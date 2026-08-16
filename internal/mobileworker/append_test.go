package mobileworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

func sha(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func newAppender(t *testing.T, repo, access string) Appender {
	t.Helper()
	return Appender{
		Authority: fakeAuthority{repoPath: repo, gen: 1, access: access},
		Reader:    SVNReader{},
		Committer: SVNAppender{},
		Ledger:    Ledger{Dir: t.TempDir()},
	}
}

func propget(t *testing.T, repo, prop, url string) string {
	t.Helper()
	out, err := exec.Command("svn", "propget", prop, url).Output()
	if err != nil {
		t.Fatalf("svn propget %s: %v", prop, err)
	}
	return strings.TrimSpace(string(out))
}

func TestAppendCommitsUniqueWithNeedsLock(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	data := []byte("brand new photo")
	rid := uuid.NewString()
	res, err := a.Upload(context.Background(), "c", rid, v1.UploadObjectPayload{
		RepoID: "r", ParentPath: "photos/2026", Filename: "new.jpg", Size: int64(len(data)), Sha256: sha(data),
	}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != v1.OutcomeCommitted || res.Revision != 2 || res.FinalPath != "photos/2026/new.jpg" {
		t.Fatalf("unexpected result %+v", res)
	}
	// svn:needs-lock set on the new object.
	if got := propget(t, repo, "svn:needs-lock", fileURL(repo)+"/photos/2026/new.jpg"); got != "*" {
		t.Fatalf("needs-lock = %q, want *", got)
	}
	// Ledger records COMMITTED with the revision.
	rec, err := a.Ledger.Lookup(rid)
	if err != nil || rec == nil {
		t.Fatalf("ledger lookup: %v rec=%v", err, rec)
	}
	if rec.State != v1.OpStateCommitted || rec.Revision != 2 {
		t.Fatalf("ledger record = %+v", rec)
	}
}

func TestAppendIdempotentReplay(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	data := []byte("same bytes")
	rid := uuid.NewString()
	p := v1.UploadObjectPayload{RepoID: "r", ParentPath: "photos", Filename: "one.bin", Size: int64(len(data)), Sha256: sha(data)}

	first, err := a.Upload(context.Background(), "c", rid, p, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Upload(context.Background(), "c", rid, p, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 2 || second.Revision != 2 || second.Outcome != v1.OutcomeCommitted {
		t.Fatalf("replay not idempotent: first=%+v second=%+v", first, second)
	}
	// No second commit happened.
	r := SVNReader{}
	if rev, _ := r.Youngest(context.Background(), repo); rev != 2 {
		t.Fatalf("HEAD advanced to %d on replay", rev)
	}
}

func TestAppendCollisionSameAndDiff(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	original := []byte("the original content")
	a.mustUpload(t, "photos", "shared.bin", original) // rev 2

	// Identical content under the same name -> drop (SAME).
	same := a.mustUpload(t, "photos", "shared.bin", original)
	if same.Outcome != v1.OutcomeNameTakenSame || same.ExistingSha256 != sha(original) {
		t.Fatalf("expected NAME_TAKEN_SAME, got %+v", same)
	}

	// Different content under the same name -> user decides (DIFF).
	diff := a.mustUpload(t, "photos", "shared.bin", []byte("totally different"))
	if diff.Outcome != v1.OutcomeNameTakenDiff || diff.ExistingSha256 != sha(original) {
		t.Fatalf("expected NAME_TAKEN_DIFF, got %+v", diff)
	}

	// Neither collision published a revision.
	r := SVNReader{}
	if rev, _ := r.Youngest(context.Background(), repo); rev != 2 {
		t.Fatalf("collision advanced HEAD to %d", rev)
	}
}

func (a Appender) mustUpload(t *testing.T, parent, name string, data []byte) v1.UploadObjectResult {
	t.Helper()
	res, err := a.Upload(context.Background(), "c", uuid.NewString(), v1.UploadObjectPayload{
		RepoID: "r", ParentPath: parent, Filename: name, Size: int64(len(data)), Sha256: sha(data),
	}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAppendCreatesMissingMobileUploadsTree(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")
	data := []byte("z telefonu")
	res := a.mustUpload(t, "mobile-uploads/Wakacje", "foto.jpg", data)
	if res.Outcome != v1.OutcomeCommitted || res.FinalPath != "mobile-uploads/Wakacje/foto.jpg" {
		t.Fatalf("missing-parent upload: %+v", res)
	}
	kind, exists, err := SVNReader{}.Stat(context.Background(), repo, "mobile-uploads/Wakacje", res.Revision)
	if err != nil || !exists || kind != v1.KindDirectory {
		t.Fatalf("created parent: exists=%v kind=%q err=%v", exists, kind, err)
	}
}

func TestAppendDestinationGoneWhenParentIsAFile(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	res := a.mustUpload(t, "top.txt", "x.bin", []byte("data"))
	if res.Outcome != v1.OutcomeDestGone {
		t.Fatalf("expected DESTINATION_GONE when parent is a file, got %+v", res)
	}
}

func TestAppendRejectsHashMismatch(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "rw")

	data := []byte("actual bytes")
	_, err := a.Upload(context.Background(), "c", uuid.NewString(), v1.UploadObjectPayload{
		RepoID: "r", ParentPath: "photos", Filename: "y.bin", Size: int64(len(data)), Sha256: sha([]byte("wrong")),
	}, bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected sha256 mismatch rejection")
	}
}

func TestFileURLAtEncodesPolishSegments(t *testing.T) {
	got := fileURLAt("/var/filees/repositories/repo", "00_Materiały-wyjsciowe/a.jpg")
	if !strings.Contains(got, "00_Materia%C5%82y-wyjsciowe/a.jpg") {
		t.Fatalf("fileURLAt = %q, want percent-encoded ł", got)
	}
	if fileURLAt("/var/filees/repositories/repo", "") != fileURL("/var/filees/repositories/repo") {
		t.Fatal("empty rel must stay the repository root URL")
	}
}

func TestAppendIntoPolishDirectory(t *testing.T) {
	requireSVN(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	run(t, "svnadmin", "create", repo)
	seed := filepath.Join(dir, "seed")
	writeSeed(t, seed, "00_Materiały-wyjsciowe/keep.txt", []byte("keep"))
	run(t, "svn", "import", "-q", seed, fileURL(repo), "-m", "polish parent")
	a := newAppender(t, repo, "rw")
	data := []byte("z telefonu")
	res := a.mustUpload(t, "00_Materiały-wyjsciowe", "foto.jpg", data)
	if res.Outcome != v1.OutcomeCommitted || res.FinalPath != "00_Materiały-wyjsciowe/foto.jpg" {
		t.Fatalf("upload into Polish parent: %+v", res)
	}
}

func TestAppendRequiresReadWrite(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	a := newAppender(t, repo, "r") // read-only grant

	res := a.mustUpload(t, "photos", "z.bin", []byte("data"))
	if res.Outcome != v1.OutcomeAccessRevoked {
		t.Fatalf("expected ACCESS_REVOKED for non-rw, got %+v", res)
	}
}
