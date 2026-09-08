package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"filees/internal/svnurl"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/passport"
	"filees/pkg/pathownership"
	"github.com/google/uuid"
)

func TestSVNPathOwnersRenameForkRevokeAndRegrant(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	f := newReplacementFixture(t)
	svn, _ := exec.LookPath("svn")
	source := SVNPathOwners{SVN: svn, RepositoriesRoot: f.authority.Locks.RepositoriesRoot, ServiceWC: f.authority.ServiceWC, CacheRoot: t.TempDir()}
	repo := filepath.Join(source.RepositoriesRoot, f.req.RepoID)
	wc := filepath.Join(t.TempDir(), "wc")
	replacementCommand(t, "svnadmin", "create", repo)
	replacementCommand(t, svn, "co", svnurl.File(repo), wc)
	creator := uuid.NewString()
	f.client(t, creator, f.guest, "active")
	f.put(t, filepath.Join("admin", "repositories", f.req.RepoID+".json"), repositoryRecord{Schema: RepositorySchema, RepoID: f.req.RepoID, OwnerRealmID: f.owner, State: "active", DisplayName: "Ownership fixture", URL: "svn+ssh://_filees-data@example/" + f.req.RepoID, CreatedAt: time.Now()})
	for id, realm := range map[string]string{f.holder: f.owner, creator: f.guest} {
		view := clientview.View{Schema: clientview.Schema, ClientID: id, RealmID: realm, ClientRole: "normal", ServerDisplayName: "Fixture", Generation: 1, GeneratedAt: time.Now(), Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, err := clientview.StoreIfNewer(filepath.Join(source.ServiceWC, "clients", id, "view.json"), view); err != nil {
			t.Fatal(err)
		}
	}
	publisher := ServicePublisher{ServiceWC: source.ServiceWC, DataAuthzFile: filepath.Join(t.TempDir(), "data.authz"), Runner: &publishRunner{}, RepositoryHead: source.Head}
	grant, err := publisher.Grant(t.Context(), f.owner, f.guest, f.req.RepoID, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if grant.PathOwnerCutoffRevision == nil || *grant.PathOwnerCutoffRevision != 0 || grant.PathOwnerRepositoryUUID == "" {
		t.Fatalf("new grant has no boundary: %+v", grant)
	}
	write := func(rel string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wc, rel), []byte("local data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(author string) {
		t.Helper()
		replacementCommand(t, svn, "ci", "--username", author, "-m", "ownership fixture", wc)
		replacementCommand(t, svn, "up", wc)
	}
	read := func() map[string]pathownership.Entry {
		t.Helper()
		snapshot, err := source.Snapshot(t.Context(), f.req.RepoID)
		if err != nil {
			t.Fatal(err)
		}
		entries := map[string]pathownership.Entry{}
		for _, entry := range snapshot.Entries {
			entries[entry.Path] = entry
		}
		return entries
	}
	write("first")
	replacementCommand(t, svn, "add", filepath.Join(wc, "first"))
	commit(creator)
	first := read()["first"]
	if first.FirstCommitter != creator || first.OwnerRealmID != f.guest {
		t.Fatalf("creator is not owner: %+v", first)
	}
	// Another administrator's rename is not a new first commit.
	replacementCommand(t, svn, "mv", filepath.Join(wc, "first"), filepath.Join(wc, "renamed"))
	commit(f.holder)
	renamed := read()["renamed"]
	if renamed.Object != first.Object || renamed.OwnerRealmID != f.guest {
		t.Fatalf("rename lost continuity: %+v", renamed)
	}
	replacementCommand(t, svn, "cp", filepath.Join(wc, "renamed"), filepath.Join(wc, "fork"))
	commit(f.holder)
	fork := read()["fork"]
	if fork.FirstCommitter != f.holder || fork.OwnerRealmID != f.owner || fork.ID == first.ID {
		t.Fatalf("fork inherited source: %+v", fork)
	}
	replacementCommand(t, svn, "rm", filepath.Join(wc, "renamed"))
	commit(f.holder)
	if after := read()["fork"]; after != fork {
		t.Fatalf("later delete rewrote fork: %+v", after)
	}
	write("guest")
	replacementCommand(t, svn, "add", filepath.Join(wc, "guest"))
	commit(creator)
	original := read()["guest"]
	if original.OwnerRealmID != f.guest {
		t.Fatal("guest did not own new file")
	}
	// Removing an installation must not erase the originating realm.
	f.client(t, creator, f.guest, "revoked")
	if read()["guest"] != original {
		t.Fatal("installation revoke changed object owner")
	}
	if _, err := publisher.RevokeGrant(t.Context(), f.owner, f.guest, f.req.RepoID); err != nil {
		t.Fatal(err)
	}
	revoked := read()["guest"]
	if revoked.OwnerRealmID != f.owner || revoked.Object != original.Object {
		t.Fatal("grant revoke lost provenance or did not reassign rights")
	}
	if _, err := publisher.Grant(t.Context(), f.owner, f.guest, f.req.RepoID, "rw"); err != nil {
		t.Fatal(err)
	}
	if read()["guest"].OwnerRealmID != f.owner {
		t.Fatal("regrant resurrected old ownership")
	}
	// Moving an old incarnation past the regrant boundary cannot launder it.
	replacementCommand(t, svn, "mv", filepath.Join(wc, "guest"), filepath.Join(wc, "guest-renamed"))
	commit(f.holder)
	if e := read()["guest-renamed"]; e.Object != original.Object || e.OwnerRealmID != f.owner {
		t.Fatal("rename bypassed revoke boundary")
	}
	freshClient := uuid.NewString()
	f.client(t, freshClient, f.guest, "active")
	replacementCommand(t, svn, "cp", filepath.Join(wc, "guest-renamed"), filepath.Join(wc, "new-fork"))
	commit(freshClient)
	if e := read()["new-fork"]; e.OwnerRealmID != f.guest || e.ID == original.ID {
		t.Fatal("new post-regrant fork failed to acquire its own origin")
	}
	// The resolver is disposable; recreating it yields the same canonical facts.
	restarted := SVNPathOwners{SVN: source.SVN, RepositoriesRoot: source.RepositoriesRoot, ServiceWC: source.ServiceWC}
	got, err := restarted.PathOwner(t.Context(), f.req.RepoID, "new-fork")
	if err != nil || got != f.guest {
		t.Fatalf("restart owner=%q err=%v", got, err)
	}
	// Use the real derived owner in the existing conditional mutation
	// boundary, not a test-supplied answer or a realm claimed in the comment.
	admin, _ := exec.LookPath("svnadmin")
	newInstallation := uuid.NewString()
	f.client(t, newInstallation, f.guest, "active")
	token := "opaquelocktoken:" + uuid.NewString()
	metadata := passport.Metadata{PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), RealmID: f.owner, IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Minute), HardExpiresAt: time.Now().Add(time.Hour)}
	comment := filepath.Join(t.TempDir(), "comment")
	if err := os.WriteFile(comment, []byte(passport.FormatComment(metadata)), 0600); err != nil {
		t.Fatal(err)
	}
	replacementCommand(t, admin, "lock", repo, "/new-fork", freshClient, comment, token)
	authority := PassportReplacementAuthority{ServiceWC: source.ServiceWC, PathOwners: source, Locks: SVNAdminLockAuthority{SVNAdmin: admin, RepositoriesRoot: source.RepositoriesRoot}}
	req := PassportReplacement{RepoID: f.req.RepoID, Path: "new-fork", ObservedToken: token, PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), Mode: "migrate"}
	if err := authority.Prepare(t.Context(), f.session, req); !errors.Is(err, ErrPassportReplacementDenied) {
		t.Fatalf("repo owner took guest object: %v", err)
	}
	guestSession := Session{ClientID: newInstallation, RealmID: f.guest, Repositories: []clientview.Repository{{RepoID: f.req.RepoID, State: "active", Access: "rw"}}}
	if err := authority.Prepare(t.Context(), guestSession, req); err != nil {
		t.Fatalf("path owner migration failed: %v", err)
	}
	if lock, err := authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, "new-fork"); err != nil || lock != nil {
		t.Fatalf("conditional release failed: %+v %v", lock, err)
	}
	beforeReplacement := read()["new-fork"]
	replacementCommand(t, admin, "setuuid", repo, uuid.NewString())
	if afterReplacement := read()["new-fork"]; afterReplacement.ID == beforeReplacement.ID || afterReplacement.OwnerRealmID != f.owner {
		t.Fatal("ownership escaped its SVN repository incarnation")
	}
	// Old canonical grants with no epoch remain readable for access control,
	// but cannot silently confer uncertain path ownership after upgrade.
	path, _ := realmGrantPath(source.ServiceWC, f.req.RepoID, f.guest)
	var stored RealmGrantRecord
	if err := decodeJSONFile(path, &stored); err != nil {
		t.Fatal(err)
	}
	stored.PathOwnerCutoffRevision = nil
	stored.PathOwnerRepositoryUUID = ""
	if err := atomicJSON(path, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Snapshot(t.Context(), f.req.RepoID); !errors.Is(err, ErrPathOwnerUnavailable) {
		t.Fatalf("guessed legacy ownership: %v", err)
	}
}

func TestGrantRegrantCannotWriteWithoutTrustedBoundary(t *testing.T) {
	for _, scenario := range []string{"unavailable", "failed", "negative", "invalid-incarnation", "rewound"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReplacementFixture(t)
			f.grant(t, "", "revoked")
			grantPath, _ := realmGrantPath(f.authority.ServiceWC, f.req.RepoID, f.guest)
			var record RealmGrantRecord
			if err := decodeJSONFile(grantPath, &record); err != nil {
				t.Fatal(err)
			}
			cutoff := int64(9)
			incarnation := uuid.NewString()
			record.PathOwnerCutoffRevision = &cutoff
			record.PathOwnerRepositoryUUID = incarnation
			if err := atomicJSON(grantPath, record); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(grantPath)
			p := ServicePublisher{ServiceWC: f.authority.ServiceWC, DataAuthzFile: filepath.Join(t.TempDir(), "data.authz"), Runner: &publishRunner{}}
			if scenario != "unavailable" {
				p.RepositoryHead = func(context.Context, string) (RepositoryRevision, error) {
					switch scenario {
					case "failed":
						return RepositoryRevision{}, errors.New("offline")
					case "negative":
						return RepositoryRevision{Number: -1, UUID: incarnation}, nil
					case "invalid-incarnation":
						return RepositoryRevision{Number: 10, UUID: "bad"}, nil
					default:
						return RepositoryRevision{Number: 8, UUID: incarnation}, nil
					}
				}
			}
			if _, err := p.Grant(t.Context(), f.owner, f.guest, f.req.RepoID, "rw"); err == nil {
				t.Fatal("untrusted boundary accepted")
			}
			after, _ := os.ReadFile(grantPath)
			if string(before) != string(after) {
				t.Fatal("failed regrant changed canonical record")
			}
		})
	}
}

func TestOwnershipGrantRecordValidation(t *testing.T) {
	r := RealmGrantRecord{Schema: RealmGrantSchema, RepoID: uuid.NewString(), OwnerRealmID: uuid.NewString(), RecipientRealmID: uuid.NewString(), State: "revoked", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	negative := int64(-1)
	r.PathOwnerCutoffRevision = &negative
	if validateRealmGrantRecord(r) == nil {
		t.Fatal("negative cutoff accepted")
	}
	r.PathOwnerCutoffRevision = nil
	r.PathOwnerRepositoryUUID = uuid.NewString()
	if validateRealmGrantRecord(r) == nil {
		t.Fatal("incarnation without boundary accepted")
	}
}

func TestOwnershipOutputCannotBypassBoundThroughReadFrom(t *testing.T) {
	if _, ok := any(&ownershipOutput{}).(io.ReaderFrom); ok {
		t.Fatal("io.Copy can bypass bounded Write")
	}
}
