package servertool

import (
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/clientview"
)

// Which repositories count as written to by more than one realm: the rule the
// mobile worker uses to refuse an unattended upload
// (implementation notes (not distributed)).
func TestMobileSharedWrite(t *testing.T) {
	const (
		own     = "11111111-1111-4111-8111-111111111111"
		other   = "22222222-2222-4222-8222-222222222222"
		repoID  = "33333333-3333-4333-8333-333333333333"
		grantee = "44444444-4444-4444-8444-444444444444"
	)
	grant := func(t *testing.T, serviceWC, name, body string) {
		t.Helper()
		dir := filepath.Join(serviceWC, "admin", "grants", repoID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		repo   clientview.Repository
		grants map[string]string
		want   bool
	}{
		{name: "own repository, never granted", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: own, Access: "rw"}},
		{name: "own repository, read-only guest", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: own, Access: "rw"},
			grants: map[string]string{grantee + ".json": `{"state":"active","access":"r"}`}},
		{name: "own repository, revoked write guest", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: own, Access: "rw"},
			grants: map[string]string{grantee + ".json": `{"state":"revoked"}`}},
		{name: "own repository, active write guest", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: own, Access: "rw"},
			grants: map[string]string{grantee + ".json": `{"state":"active","access":"rw"}`}, want: true},
		{name: "own repository, unreadable grant record", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: own, Access: "rw"},
			grants: map[string]string{grantee + ".json": `{not json`}, want: true},
		{name: "guest with write access", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: other, Access: "rw"}, want: true},
		{name: "guest with read access", repo: clientview.Repository{RepoID: repoID, OwnerRealmID: other, Access: "r"}},
		{name: "legacy view without an owner", repo: clientview.Repository{RepoID: repoID, Access: "rw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serviceWC := t.TempDir()
			for name, body := range tc.grants {
				grant(t, serviceWC, name, body)
			}
			a := clientviewMobileAuthority{ServiceWorkingCopy: serviceWC}
			if got := a.sharedWrite(own, tc.repo); got != tc.want {
				t.Fatalf("sharedWrite = %v, want %v", got, tc.want)
			}
		})
	}
}
