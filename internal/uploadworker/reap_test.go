package uploadworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/avscan"
	"filees/pkg/realmbranding"
	"filees/pkg/repoworker"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type fakeAuthority struct{ owner, repo, alias string }

func (a fakeAuthority) OwnsActiveRepository(realmID, repoID string) error {
	if realmID != a.owner || repoID != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a fakeAuthority) ActiveRealmAlias(realmID string) (string, error) {
	if realmID != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}
func (a fakeAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

func fixture(t *testing.T, verdict avscan.Verdict) (Reaper, intake.Record, *[]string) {
	t.Helper()
	owner, authorityRepo, uploadRepo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &channel.Store{
		Root: t.TempDir(), Authority: fakeAuthority{owner: owner, repo: authorityRepo, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)),
	}
	declaration := manifest.Upload{
		OwnerRealm: owner, AuthorityRepoID: authorityRepo, UploadRepoID: uploadRepo,
		Slug: "oferta-a", Recipients: []string{"a@example.com"}, CollisionPolicy: manifest.CollisionDeny,
	}
	created, _, err := store.CreateUpload(uuid.NewString(), owner, declaration)
	if err != nil {
		t.Fatal(err)
	}
	intakeStore := intake.Store{Root: t.TempDir(), MaxBytes: 1024, Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }}
	job, err := intakeStore.Accept(created.ChannelID, created.Alias, created.Slug, strings.Repeat("ab", 32), "Opinia Łódź.pdf", bytes.NewReader([]byte("wniesiony")))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	reaper := Reaper{
		Intake: intakeStore, Channels: store, ReposRoot: t.TempDir(), TrashRoot: t.TempDir(),
		Scanner: avscan.Static{Verdict: verdict, Detail: "Eicar-Test-Signature"},
		Publisher: Publisher{SVNMucc: filepath.Join(t.TempDir(), "svnmucc"), SVNLook: filepath.Join(t.TempDir(), "svnlook"), Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if strings.Contains(name, "svnlook") {
				return []byte("/\n"), nil
			}
			return nil, nil
		}},
		Now: func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
	}
	return reaper, job, &calls
}

func TestReapCleanCommitUsesNamingPolicy(t *testing.T) {
	reaper, job, calls := fixture(t, avscan.Clean)
	summary, err := reaper.Reap(context.Background())
	if err != nil || summary.Accepted != 1 || summary.Rejected != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "put") || !strings.Contains(joined, "Opinia-Lodz.pdf") || !strings.Contains(joined, job.UploadID) {
		t.Fatalf("svnmucc calls:\n%s", joined)
	}
	if _, err := os.Stat(reaper.Intake.PayloadPath(job.UploadID)); !os.IsNotExist(err) {
		t.Fatal("clean intake was not removed")
	}
}

func TestReapInfectedGoesToTrashWaitingRoom(t *testing.T) {
	reaper, job, calls := fixture(t, avscan.Infected)
	summary, err := reaper.Reap(context.Background())
	if err != nil || summary.Rejected != 1 || summary.Accepted != 0 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	waiting := filepath.Join(reaper.TrashRoot, "oferta-a-"+job.ChannelID, "2026-08-22", job.UploadID)
	if _, err := os.Stat(filepath.Join(waiting, "payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(waiting, "index.json")); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "index.json") || !strings.Contains(joined, repoworker.UploadTrashRepositoryID(reaper.mustOwner(t, job.ChannelID))) {
		t.Fatalf("trash publish:\n%s", joined)
	}
}

func TestReapUnavailableScannerLeavesJob(t *testing.T) {
	reaper, job, _ := fixture(t, avscan.Unavailable)
	if _, err := reaper.Reap(context.Background()); !errors.Is(err, avscan.ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(reaper.Intake.Root, job.UploadID, "READY")); err != nil {
		t.Fatal("job was not released back to READY")
	}
}

func (r Reaper) mustOwner(t *testing.T, channelID string) string {
	t.Helper()
	record, err := r.Channels.GetUpload(channelID)
	if err != nil {
		t.Fatal(err)
	}
	return record.OwnerRealm
}
