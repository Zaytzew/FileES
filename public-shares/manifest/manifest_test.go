package manifest

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func rev(n int64) *int64 { return &n }

func validShare() Share {
	return Share{
		OwnerRealm: uuid.NewString(),
		RepoID:     uuid.NewString(),
		SourceRoot: "projekt/wydanie-pierwsze",
		Slug:       "petofiego",
		Objects: []Object{{
			PublicID:    "7f3a1c9e2b4d6a80",
			RepoPath:    "projekt/wydanie-pierwsze/PB-rzuty.pdf",
			DisplayName: "Projekt budowlany — rzuty.pdf",
		}},
	}
}

func validUpload() Upload {
	return Upload{
		OwnerRealm:      uuid.NewString(),
		AuthorityRepoID: uuid.NewString(),
		TrashRepoID:     uuid.NewString(),
		Slug:            "sggw-upload",
		Recipients:      []string{"a@example.com"},
		CollisionPolicy: CollisionDeny,
	}
}

func TestValidShareAndUpload(t *testing.T) {
	if err := validShare().Validate(); err != nil {
		t.Fatalf("share: %v", err)
	}
	if err := validUpload().Validate(); err != nil {
		t.Fatalf("upload: %v", err)
	}
}

// An upload channel is always closed. There is no configuration in which an
// anonymous contribution is accepted.
func TestUploadRequiresRecipients(t *testing.T) {
	u := validUpload()
	u.Recipients = nil
	if err := u.Validate(); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("got %v, want ErrNoRecipients", err)
	}
	u.Recipients = []string{}
	if err := u.Validate(); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("empty slice: got %v, want ErrNoRecipients", err)
	}
}

// The trash repository cannot be the source of the right to create the channel
// that creates it.
func TestUploadRejectsAuthorityEqualToTrash(t *testing.T) {
	u := validUpload()
	u.TrashRepoID = u.AuthorityRepoID
	if err := u.Validate(); !errors.Is(err, ErrSameRepo) {
		t.Fatalf("got %v, want ErrSameRepo", err)
	}
}

func TestUploadRejectsUnknownCollisionPolicy(t *testing.T) {
	u := validUpload()
	u.CollisionPolicy = "overwrite"
	if err := u.Validate(); err == nil {
		t.Fatal("silent overwrite was accepted as a policy")
	}
}

// A closed channel authenticates by token; a shared password alongside would
// reintroduce the leak tokens exist to remove.
func TestShareRejectsPasswordOnClosedChannel(t *testing.T) {
	s := validShare()
	s.Recipients = []string{"a@example.com"}
	s.Password = "argon2id$..."
	if err := s.Validate(); !errors.Is(err, ErrClosedPassword) {
		t.Fatalf("got %v, want ErrClosedPassword", err)
	}
}

func TestShareOpenChannelMayCarryPassword(t *testing.T) {
	s := validShare()
	s.Password = "argon2id$..."
	if err := s.Validate(); err != nil {
		t.Fatalf("open channel with password rejected: %v", err)
	}
	if s.Closed() {
		t.Fatal("channel without recipients reported as closed")
	}
}

func TestShareFrozen(t *testing.T) {
	s := validShare()
	if _, ok := s.Frozen(); ok {
		t.Fatal("share without do-not-follow reported as frozen")
	}
	s.DoNotFollow = rev(344)
	got, ok := s.Frozen()
	if !ok || got != 344 {
		t.Fatalf("Frozen() = %d, %v", got, ok)
	}
}

func TestShareRejectsUnpublishableRevision(t *testing.T) {
	for _, r := range []int64{0, -1} {
		s := validShare()
		s.DoNotFollow = rev(r)
		if err := s.Validate(); err == nil {
			t.Fatalf("do-not-follow = %d was accepted", r)
		}
	}
}

func TestShareRejectsDuplicatePublicID(t *testing.T) {
	s := validShare()
	s.Objects = append(s.Objects, Object{
		PublicID:    s.Objects[0].PublicID,
		RepoPath:    "projekt/inny.pdf",
		DisplayName: "Inny.pdf",
	})
	if err := s.Validate(); !errors.Is(err, ErrDuplicateObject) {
		t.Fatalf("got %v, want ErrDuplicateObject", err)
	}
}

// The manifest is data, so it gets the same treatment as any other input even
// though the public side never authors it.
func TestShareRejectsEscapingRepoPaths(t *testing.T) {
	for _, path := range []string{
		"../../etc/passwd",
		"/etc/passwd",
		"projekt/../../etc",
		"projekt//rzuty.pdf",
		"projekt\\rzuty.pdf",
		"projekt/./rzuty.pdf",
		"projekt/rzuty\x00.pdf",
		"",
	} {
		s := validShare()
		s.Objects[0].RepoPath = path
		if err := s.Validate(); err == nil {
			t.Fatalf("repo_path %q was accepted", path)
		}
	}
}

func TestShareRejectsBadPublicID(t *testing.T) {
	for _, id := range []string{
		"short",
		strings.Repeat("a", 65),
		"7f3a1c9e2b4d6a8_",
		"7F3A1C9E2B4D6A80",
		"7f3a1c9e2b4d6a8/",
		"../7f3a1c9e2b4d6a80",
	} {
		s := validShare()
		s.Objects[0].PublicID = id
		if err := s.Validate(); err == nil {
			t.Fatalf("public_id %q was accepted", id)
		}
	}
}

func TestShareRejectsControlCharsInDisplayName(t *testing.T) {
	s := validShare()
	s.Objects[0].DisplayName = "rzuty\r\n.pdf"
	if err := s.Validate(); err == nil {
		t.Fatal("display_name with control characters was accepted")
	}
}

func TestRecipientsMustBeAddresses(t *testing.T) {
	for _, addr := range []string{
		"",
		"   ",
		"nobody",
		"@example.com",
		"a@",
		"a@example.com, b@example.com",
		"a@example.com\r\nBcc: c@example.com",
	} {
		u := validUpload()
		u.Recipients = []string{addr}
		if err := u.Validate(); err == nil {
			t.Fatalf("recipient %q was accepted", addr)
		}
	}
}

func TestManifestsRejectNonUUIDIdentifiers(t *testing.T) {
	s := validShare()
	s.RepoID = "not-a-uuid"
	if err := s.Validate(); err == nil {
		t.Fatal("share accepted a non-UUID repo_id")
	}
	u := validUpload()
	u.AuthorityRepoID = "not-a-uuid"
	if err := u.Validate(); err == nil {
		t.Fatal("upload accepted a non-UUID authority_repo_id")
	}
}
