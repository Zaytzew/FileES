package channel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUploadStatusBoundToContributorAndActualReceipt(t *testing.T) {
	s := acceptedStore(t)
	c, id, token := uuid.NewString(), uuid.NewString(), strings.Repeat("a", 64)
	status, err := s.ReadUploadStatus(c, id, token)
	if err != nil || status.State != "pending" {
		t.Fatal(status, err)
	}
	if err := s.RecordUploadOutcome(c, token, UploadStatus{UploadID: id, State: "attention", Reason: "collision"}); err != nil {
		t.Fatal(err)
	}
	status, err = s.ReadUploadStatus(c, id, token)
	if err != nil || status.State != "attention" || status.Reason != "collision" {
		t.Fatal(status, err)
	}
	entries, err := s.ListAccepted(c)
	if err != nil || len(entries) != 0 {
		t.Fatal("problem appeared on shelf", entries, err)
	}
	if err := s.RecordAccepted(Accepted{ChannelID: c, UploadID: id, TokenSHA256: token, RepoPath: "a/plan.pdf", Size: 5, SHA256: strings.Repeat("b", 64), Revision: 3}); err != nil {
		t.Fatal(err)
	}
	status, err = s.ReadUploadStatus(c, id, token)
	if err != nil || status.State != "accepted" || status.Revision != 3 || status.RepoPath != "a/plan.pdf" {
		t.Fatal(status, err)
	}
	if _, err := s.ReadUploadStatus(c, id, strings.Repeat("b", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross contributor receipt", err)
	}
	if _, err := s.ReadUploadStatus(c, "../"+id, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("traversal", err)
	}
	if err := s.RecordUploadOutcome(c, token, UploadStatus{UploadID: id, State: "attention", Reason: "/var/private/details"}); err == nil {
		t.Fatal("raw diagnostic recorded")
	}
}

func TestUploadReceiptSurvivesShelfClearAndExpiresWithoutTouchingArrivals(t *testing.T) {
	s := acceptedStore(t)
	now := s.now()
	s.Now = func() time.Time { return now }
	c, id, token := uuid.NewString(), uuid.NewString(), strings.Repeat("a", 64)
	status := UploadStatus{UploadID: id, State: "accepted", RepoPath: "a.pdf", Size: 1, SHA256: strings.Repeat("b", 64), Revision: 1}
	if err := s.RecordAccepted(Accepted{ChannelID: c, UploadID: id, TokenSHA256: token, RepoPath: status.RepoPath, Size: 1, SHA256: status.SHA256, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUploadOutcome(c, token, status); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAccepted(c, []string{id}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadUploadStatus(c, id, token)
	if err != nil || got.State != "accepted" {
		t.Fatal(got, err)
	}
	arrival := uuid.NewString()
	if err := s.RecordAccepted(Accepted{ChannelID: c, UploadID: arrival, RepoPath: "other.pdf"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(uploadOutcomeLifetime + time.Second)
	if err := s.PurgeUploadOutcomes(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.acceptedDir(c), id+".outcome.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	entries, err := s.ListAccepted(c)
	if err != nil || len(entries) != 1 || entries[0].UploadID != arrival {
		t.Fatal(entries, err)
	}
}

func TestUploadStatusDoesNotFollowSymlinkOrTrustMalformedReceipt(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "symlink"}[symlink], func(t *testing.T) {
			s := acceptedStore(t)
			c, id := uuid.NewString(), uuid.NewString()
			dir := s.acceptedDir(c)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, id+".json")
			if symlink {
				if err := os.Symlink(filepath.Join(t.TempDir(), "unknown"), p); err != nil {
					t.Skip(err)
				}
			} else if err := os.WriteFile(p, []byte("not JSON"), 0600); err != nil {
				t.Fatal(err)
			}
			if status, err := s.ReadUploadStatus(c, id, strings.Repeat("a", 64)); err == nil {
				t.Fatal(status)
			}
		})
	}
}
