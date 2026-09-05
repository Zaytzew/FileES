package repoworker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

type pathOwnerFunc func(context.Context, string, string) (string, error)

func (f pathOwnerFunc) PathOwner(ctx context.Context, repo, path string) (string, error) {
	return f(ctx, repo, path)
}

type replacementFixture struct {
	authority            PassportReplacementAuthority
	session              Session
	req                  PassportReplacement
	metadata             passport.Metadata
	holder, owner, guest string
	unlocks              int
}

func newReplacementFixture(t *testing.T) *replacementFixture {
	t.Helper()
	f := &replacementFixture{owner: uuid.NewString(), guest: uuid.NewString()}
	now := time.Now().UTC()
	f.session = Session{ClientID: uuid.NewString(), RealmID: f.owner}
	f.holder = f.session.ClientID
	f.req = PassportReplacement{RepoID: uuid.NewString(), Path: "docs/file.txt", ObservedToken: "opaquelocktoken:" + uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "renew"}
	f.metadata = passport.Metadata{PassportID: f.req.PassportID, InstanceUID: f.req.InstanceUID, RealmID: f.owner, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)}
	f.session.Repositories = []clientview.Repository{{RepoID: f.req.RepoID, State: "active", Access: "rw", OwnerRealmID: f.owner}}
	f.authority = PassportReplacementAuthority{ServiceWC: t.TempDir(), Now: func() time.Time { return now }}
	f.authority.Locks = SVNAdminLockAuthority{SVNAdmin: filepath.Join(t.TempDir(), "svnadmin"), RepositoriesRoot: t.TempDir(), Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "lslocks":
			return []byte("Path: /" + f.req.Path + "\nUUID Token: " + f.req.ObservedToken + "\nOwner: " + f.holder + "\nComment (1 line):\n" + passport.FormatComment(f.metadata) + "\n"), nil
		case "unlock":
			f.unlocks++
			want := []string{"unlock", "--", filepath.Join(f.authority.Locks.RepositoriesRoot, f.req.RepoID), "/" + f.req.Path, f.holder, f.req.ObservedToken}
			if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("unsafe unlock arguments: %q", args)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected command %q", args)
			return nil, nil
		}
	}}
	f.authority.PathOwners = pathOwnerFunc(func(context.Context, string, string) (string, error) { return f.owner, nil })
	f.client(t, f.session.ClientID, f.owner, "active")
	f.realm(t, f.owner, "active")
	f.realm(t, f.guest, "active")
	f.put(t, filepath.Join("admin", "repositories", f.req.RepoID+".json"), repositoryRecord{Schema: RepositorySchema, RepoID: f.req.RepoID, OwnerRealmID: f.owner, State: "active"})
	return f
}
func (f *replacementFixture) put(t *testing.T, rel string, value any) {
	t.Helper()
	if err := atomicJSON(filepath.Join(f.authority.ServiceWC, rel), value); err != nil {
		t.Fatal(err)
	}
}
func (f *replacementFixture) client(t *testing.T, id, realm, state string) {
	t.Helper()
	f.put(t, filepath.Join("admin", "clients", id+".json"), passportClientIdentity{Schema: "filees.client-instance/v1", ClientID: id, RealmID: realm, State: state})
}
func (f *replacementFixture) realm(t *testing.T, id, state string) {
	t.Helper()
	f.put(t, filepath.Join("admin", "realms", id+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: id, State: state})
}
func (f *replacementFixture) grant(t *testing.T, access, state string) {
	t.Helper()
	grant := RealmGrantRecord{Schema: RealmGrantSchema, RepoID: f.req.RepoID, OwnerRealmID: f.owner, RecipientRealmID: f.guest, State: state, Access: access, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if access == "rw" {
		grant.PathOwnerPolicy = "first_committer"
	}
	f.put(t, filepath.Join("admin", "grants", f.req.RepoID, f.guest+".json"), grant)
}

func TestPassportReplacementAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*replacementFixture, *testing.T)
		want   bool
	}{
		{"renew own installation", func(f *replacementFixture, t *testing.T) {}, true},
		{"manual passport without realm comment", func(f *replacementFixture, t *testing.T) { f.metadata.RealmID = "" }, true},
		{"renew other installation", func(f *replacementFixture, t *testing.T) {
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.owner, "active")
		}, false},
		{"wrong passport", func(f *replacementFixture, t *testing.T) { f.metadata.PassportID = uuid.NewString() }, false},
		{"wrong WC", func(f *replacementFixture, t *testing.T) { f.metadata.InstanceUID = uuid.NewString() }, false},
		{"expired renewal", func(f *replacementFixture, t *testing.T) {
			f.authority.Now = func() time.Time { return f.metadata.ExpiresAt }
		}, false},
		{"hard expired renewal", func(f *replacementFixture, t *testing.T) {
			f.authority.Now = func() time.Time { return f.metadata.HardExpiresAt }
		}, false},
		{"read only view", func(f *replacementFixture, t *testing.T) { f.session.Repositories[0].Access = "r" }, false},
		{"missing view", func(f *replacementFixture, t *testing.T) { f.session.Repositories = nil }, false},
		{"revoked requester", func(f *replacementFixture, t *testing.T) { f.client(t, f.session.ClientID, f.owner, "revoked") }, false},
		{"revoked requester realm", func(f *replacementFixture, t *testing.T) { f.realm(t, f.owner, "revoked") }, false},
		{"forged session realm", func(f *replacementFixture, t *testing.T) { f.session.RealmID = f.guest }, false},
		{"migrate own realm", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.owner, "active")
		}, true},
		{"migrate revoked own installation", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.owner, "revoked")
		}, true},
		{"foreign holder spoofed realm comment", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.guest, "active")
		}, false},
		{"expired foreign holder", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.guest, "active")
			f.metadata.ExpiresAt = f.metadata.IssuedAt.Add(time.Second)
		}, false},
		{"unknown holder", func(f *replacementFixture, t *testing.T) { f.req.Mode = "migrate"; f.holder = uuid.NewString() }, false},
		{"missing path owner", func(f *replacementFixture, t *testing.T) { f.req.Mode = "migrate"; f.authority.PathOwners = nil }, false},
		{"repo owner cannot migrate guest file", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.authority.PathOwners = pathOwnerFunc(func(context.Context, string, string) (string, error) { return f.guest, nil })
		}, false},
		{"unknown path", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.authority.PathOwners = pathOwnerFunc(func(context.Context, string, string) (string, error) { return "", nil })
		}, false},
		{"path owner read failed", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.authority.PathOwners = pathOwnerFunc(func(context.Context, string, string) (string, error) { return "", errors.New("unavailable") })
		}, false},
		{"guest renew with canonical rw", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "rw", "active")
		}, true},
		{"stale view after grant revoke", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "", "revoked")
		}, false},
		{"stale view after rw downgrade", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "r", "active")
		}, false},
		{"unknown mode", func(f *replacementFixture, t *testing.T) { f.req.Mode = "force" }, false},
		{"guest migrates own path", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "rw", "active")
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.guest, "active")
			f.req.Mode = "migrate"
			f.authority.PathOwners = pathOwnerFunc(func(context.Context, string, string) (string, error) { return f.guest, nil })
		}, true},
		{"guest cannot migrate borrowed path across own WCs", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "rw", "active")
			f.holder = uuid.NewString()
			f.client(t, f.holder, f.guest, "active")
			f.req.Mode = "migrate"
		}, false},
		{"wrong realm schema", func(f *replacementFixture, t *testing.T) {
			f.put(t, filepath.Join("admin", "realms", f.owner+".json"), realmRecord{Schema: "unknown", RealmID: f.owner, State: "active"})
		}, false},
		{"guest in orphaned repository", func(f *replacementFixture, t *testing.T) {
			f.session.RealmID = f.guest
			f.client(t, f.session.ClientID, f.guest, "active")
			f.grant(t, "rw", "active")
			f.realm(t, f.owner, "revoked")
		}, false},
		{"malformed passport identity", func(f *replacementFixture, t *testing.T) {
			f.req.Mode = "migrate"
			f.metadata.PassportID = "not-a-uuid"
		}, false},
		{"malformed passport timeline", func(f *replacementFixture, t *testing.T) { f.metadata.HardExpiresAt = f.metadata.IssuedAt }, false},
		{"path traversal", func(f *replacementFixture, t *testing.T) { f.req.Path = ".." }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplacementFixture(t)
			tc.change(f, t)
			err := f.authority.Prepare(t.Context(), f.session, f.req)
			if (err == nil) != tc.want {
				t.Fatalf("allowed=%v, want %v: %v", err == nil, tc.want, err)
			}
			if f.unlocks != boolInt(tc.want) {
				t.Fatalf("unlock calls=%d, allowed=%v", f.unlocks, tc.want)
			}
		})
	}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
