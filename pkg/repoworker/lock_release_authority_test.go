package repoworker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSVNAdminLockAuthorityInspectsExactPathAndPassportRealm(t *testing.T) {
	repoID := uuid.NewString()
	holderID := uuid.NewString()
	realmID := uuid.NewString()
	token := "opaquelocktoken:" + uuid.NewString()
	comment := `filees.edit-passport/v1 {"passport_id":"` + uuid.NewString() + `","instance_uid":"` + uuid.NewString() + `","realm_id":"` + realmID + `","issued_at":"2026-08-31T06:00:00Z","expires_at":"2026-08-31T09:00:00Z","hard_expires_at":"2026-08-31T12:00:00Z"}`
	output := "Path: /projekty/model.dwg\nUUID Token: " + token + "\nOwner: " + holderID + "\nCreated: 2026-08-31 09:00:00 +0200\nExpires: \nComment (1 line):\n" + comment + "\n"
	root := t.TempDir()
	var command string
	var args []string
	authority := SVNAdminLockAuthority{
		SVNAdmin: "/usr/local/bin/svnadmin", RepositoriesRoot: root,
		Run: func(_ context.Context, name string, callArgs ...string) ([]byte, error) {
			command, args = name, append([]string(nil), callArgs...)
			return []byte(output), nil
		},
	}
	got, err := authority.InspectLock(context.Background(), repoID, "projekty/model.dwg")
	if err != nil || got == nil || got.ObservedLockID != token || got.HolderClientID != holderID || got.HolderRealmID != realmID {
		t.Fatalf("observation = %+v err=%v", got, err)
	}
	wantRepo := filepath.Join(root, repoID)
	if command != authority.SVNAdmin || len(args) != 3 || args[0] != "lslocks" || args[1] != wantRepo || args[2] != "/projekty/model.dwg" {
		t.Fatalf("command = %q args=%q", command, args)
	}
}

func TestSVNAdminLockAuthorityReturnsNilForAbsentLock(t *testing.T) {
	authority := SVNAdminLockAuthority{
		SVNAdmin: "/usr/bin/svnadmin", RepositoriesRoot: t.TempDir(),
		Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	got, err := authority.InspectLock(context.Background(), uuid.NewString(), "file.txt")
	if err != nil || got != nil {
		t.Fatalf("absent observation = %+v err=%v", got, err)
	}
}

func TestSVNAdminLockAuthorityRejectsUnexpectedOwnerPathAndMultiplicity(t *testing.T) {
	repoID := uuid.NewString()
	token := "opaquelocktoken:" + uuid.NewString()
	for name, output := range map[string]string{
		"owner":    "Path: /file.txt\nUUID Token: " + token + "\nOwner: _filees-client\nComment (0 lines):\n",
		"path":     "Path: /other.txt\nUUID Token: " + token + "\nOwner: " + uuid.NewString() + "\nComment (0 lines):\n",
		"multiple": "Path: /file.txt\nUUID Token: " + token + "\nOwner: " + uuid.NewString() + "\nComment (0 lines):\nPath: /other.txt\nUUID Token: " + token + "\nOwner: " + uuid.NewString() + "\nComment (0 lines):\n",
	} {
		t.Run(name, func(t *testing.T) {
			authority := SVNAdminLockAuthority{
				SVNAdmin: "/usr/bin/svnadmin", RepositoriesRoot: t.TempDir(),
				Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(output), nil },
			}
			if got, err := authority.InspectLock(context.Background(), repoID, "file.txt"); err == nil || got != nil {
				t.Fatalf("invalid observation = %+v err=%v", got, err)
			}
		})
	}
}

func TestSVNAdminLockAuthorityDoesNotExposeCommandOutputOnFailure(t *testing.T) {
	authority := SVNAdminLockAuthority{
		SVNAdmin: "/usr/bin/svnadmin", RepositoriesRoot: t.TempDir(),
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("secret-token"), errors.New("exit 1")
		},
	}
	_, err := authority.InspectLock(context.Background(), uuid.NewString(), "file.txt")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe command error = %v", err)
	}
}
