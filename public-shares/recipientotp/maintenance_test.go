package recipientotp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func TestSweepOTPRespectsExpiryAndChannelAuthority(t *testing.T) {
	if !repoworker.FileLocksSupported() {
		t.Skip("server maintenance requires kernel locks")
	}
	for _, scenario := range []string{"active", "exhausted", "expired", "revoked", "deleted", "removed-recipient"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
			if err := f.service.RequestCode(request); err != nil {
				t.Fatal(err)
			}
			id := f.storeID(t)
			dir := filepath.Join(f.service.Root, id)
			switch scenario {
			case "exhausted": // Persist the limit explicitly; preserve it until expiry.
				_, _, digest, _ := f.service.recipient(request)
				record, err := f.service.load(id, digest)
				if err != nil {
					t.Fatal(err)
				}
				record.FailedAttempts = DefaultAttempts
				if err := f.service.store(record); err != nil {
					t.Fatal(err)
				}
			case "expired":
				*f.now = f.now.Add(DefaultTTL)
			case "revoked":
				if _, err := f.store.Revoke(f.owner, id); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if _, err := f.store.Delete(f.owner, id); err != nil {
					t.Fatal(err)
				}
			case "removed-recipient":
				updated := f.share
				updated.Recipients = nil
				if _, _, err := f.store.Update(uuid.NewString(), f.owner, id, updated); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.service.Sweep(context.Background(), *f.now)
			if err != nil {
				t.Fatal(err)
			}
			keep := scenario == "active" || scenario == "exhausted"
			if keep {
				if got.Active != 1 || got.Files != 0 {
					t.Fatalf("live state removed: %+v", got)
				}
			} else {
				if got.Entries != 1 || got.Files != 1 {
					t.Fatalf("not removed: %+v", got)
				}
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("channel directory survived: %v", err)
				}
				if scenario != "expired" {
					if err := f.service.RequestCode(request); !errors.Is(err, ErrDenied) {
						t.Fatal("closed invitation recreated state", err)
					}
				}
			}
		})
	}
}

func TestSweepOTPRefusesUnsafeFilesAndKeepsUnknown(t *testing.T) {
	if !repoworker.FileLocksSupported() {
		t.Skip("server maintenance requires kernel locks")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	id := f.storeID(t)
	dir := filepath.Join(f.service.Root, id)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".otp-123.tmp")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "notes")
	os.WriteFile(unknown, []byte("keep"), 0600)
	if _, err := f.service.Sweep(context.Background(), *f.now); err == nil {
		t.Fatal("symlink accepted")
	}
	raw, _ := os.ReadFile(outside)
	if string(raw) != "keep" {
		t.Fatal("outside changed")
	}
	os.Remove(link)
	os.WriteFile(link, []byte("interrupted write"), 0600)
	got, err := f.service.Sweep(context.Background(), *f.now)
	if err != nil || got.Files != 1 || got.Active != 1 {
		t.Fatalf("temp cleanup: %+v %v", got, err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unknown file removed")
	}
}

func TestSweepOTPDoesNotWaitForAnActiveRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server flock is Unix only")
	}
	f := newFixture(t)
	if err := f.service.validate(); err != nil {
		t.Fatal(err)
	}
	err := repoworker.WithFileLock(filepath.Join(f.service.Root, ".lock"), func() error {
		done := make(chan error, 1)
		go func() { _, err := f.service.Sweep(context.Background(), *f.now); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("busy lock accepted")
			}
		case <-time.After(time.Second):
			t.Error("maintenance waited behind request")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSweepOTPTracksUploadChannelRevocation(t *testing.T) {
	if !repoworker.FileLocksSupported() {
		t.Skip("server maintenance requires kernel locks")
	}
	f := newFixture(t)
	record, deliveries, err := f.store.CreateUpload(uuid.NewString(), f.owner, manifest.Upload{OwnerRealm: f.owner, AuthorityRepoID: f.share.RepoID, UploadRepoID: uuid.NewString(), Slug: "upload", Recipients: []string{"recipient@example.test"}, CollisionPolicy: manifest.CollisionDeny})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Alias: "realm", Slug: "upload", Invitation: deliveries[0].Token, Email: "recipient@example.test"}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	if result, err := f.service.Sweep(context.Background(), *f.now); err != nil || result.Active != 1 || result.Files != 0 {
		t.Fatalf("active upload: %+v %v", result, err)
	}
	if _, err := f.store.RevokeUpload(f.owner, record.ChannelID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.service.Sweep(context.Background(), *f.now); err != nil || result.Entries != 1 {
		t.Fatalf("revoked upload: %+v %v", result, err)
	}
}
