package mobileworker

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
)

// sharedAuthority is fakeAuthority for a repository another realm may also
// write to.
type sharedAuthority struct{ fakeAuthority }

func (s sharedAuthority) Resolve(ctx context.Context, clientID, repoID string) (View, error) {
	view, err := s.fakeAuthority.Resolve(ctx, clientID, repoID)
	view.SharedWrite = true
	return view, err
}

func commitLogOf(t *testing.T, repo string, rev int64) string {
	t.Helper()
	out, err := exec.Command("svnlook", "log", "-r", strconv.FormatInt(rev, 10), repo).Output()
	if err != nil {
		t.Fatalf("svnlook log r%d: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// An unattended upload opens its commit log with the marker; a chosen one
// keeps the plain log (implementation notes (not distributed)).
func TestAutomaticUploadsOpenTheCommitLogWithTheMarker(t *testing.T) {
	requireSVN(t)
	repo := newUploadSeededRepo(t)
	a := newAppender(t, repo, "rw")

	data := []byte("watched photo")
	object := func(name string, automatic bool) v1.UploadObjectResult {
		t.Helper()
		res, err := a.Upload(context.Background(), "c", uuid.NewString(), v1.UploadObjectPayload{
			RepoID: "r", ParentPath: "mobile-uploads/photos", Filename: name, Size: int64(len(data)), Sha256: sha(data), Automatic: automatic,
		}, bytes.NewReader(data))
		if err != nil || res.Outcome != v1.OutcomeCommitted {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		return res
	}
	if got := commitLogOf(t, repo, object("auto.jpg", true).Revision); got != v1.AutomaticMarker+" mobile append" {
		t.Fatalf("automatic object log = %q", got)
	}
	if got := commitLogOf(t, repo, object("chosen.jpg", false).Revision); got != "mobile append" {
		t.Fatalf("chosen object log = %q", got)
	}

	tree := func(name string, automatic bool) v1.UploadTreeResult {
		t.Helper()
		body := packTree(t, map[string][]byte{name: []byte(name)})
		res, err := a.UploadTree(context.Background(), "c", uuid.NewString(), v1.UploadTreePayload{
			RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body), Automatic: automatic,
		}, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res
	}
	if got := commitLogOf(t, repo, tree("auto.txt", true).Revision); got != v1.AutomaticMarker+" mobile tree ingest" {
		t.Fatalf("automatic tree log = %q", got)
	}
	if got := commitLogOf(t, repo, tree("chosen.txt", false).Revision); got != "mobile tree ingest" {
		t.Fatalf("chosen tree log = %q", got)
	}
}

// Where another realm may also write, an unattended upload is refused before
// anything is committed; a chosen upload into the same repository goes through.
func TestAutomaticUploadIsRefusedWhereAnotherRealmWrites(t *testing.T) {
	requireSVN(t)
	repo := newUploadSeededRepo(t)
	a := newAppender(t, repo, "rw")
	a.Authority = sharedAuthority{fakeAuthority{repoPath: repo, gen: 1, access: "rw"}}
	reader := SVNReader{}
	before, err := reader.Youngest(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("from the other realm's neighbour")
	payload := v1.UploadObjectPayload{RepoID: "r", ParentPath: "mobile-uploads/photos", Filename: "shared.jpg", Size: int64(len(data)), Sha256: sha(data), Automatic: true}
	if _, err := a.Upload(context.Background(), "c", uuid.NewString(), payload, bytes.NewReader(data)); !errors.Is(err, ErrAutomaticShared) {
		t.Fatalf("automatic object into a shared repository: %v", err)
	}
	body := packTree(t, map[string][]byte{"shared.txt": []byte("x")})
	treePayload := v1.UploadTreePayload{RepoID: "r", ParentPath: "mobile-uploads", FileCount: 1, Size: int64(len(body)), Sha256: sha(body), Automatic: true}
	if _, err := a.UploadTree(context.Background(), "c", uuid.NewString(), treePayload, bytes.NewReader(body)); !errors.Is(err, ErrAutomaticShared) {
		t.Fatalf("automatic tree into a shared repository: %v", err)
	}
	if after, _ := reader.Youngest(context.Background(), repo); after != before {
		t.Fatalf("a refused automatic upload advanced HEAD %d -> %d", before, after)
	}

	payload.Automatic = false
	res, err := a.Upload(context.Background(), "c", uuid.NewString(), payload, bytes.NewReader(data))
	if err != nil || res.Outcome != v1.OutcomeCommitted {
		t.Fatalf("chosen upload into a shared repository: %+v %v", res, err)
	}
}

// The phone gets a named code, not the generic failure.
func TestAutomaticSharedRefusalHasItsOwnCode(t *testing.T) {
	var out bytes.Buffer
	req, err := v1.NewRequest(uuid.NewString(), v1.OpUploadTree, v1.UploadTreePayload{RepoID: uuid.NewString(), ParentPath: "mobile-uploads", FileCount: 1, Size: 1, Sha256: sha([]byte("x")), Automatic: true})
	if err != nil {
		t.Fatal(err)
	}
	d := Dispatcher{Appender: Appender{Ledger: Ledger{Dir: t.TempDir()}}}
	if err := d.writeError(&out, req, ErrAutomaticShared); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), v1.ErrCodeAutomaticShared) || strings.Contains(out.String(), "worker.failed") {
		t.Fatalf("response = %q", out.String())
	}
}
