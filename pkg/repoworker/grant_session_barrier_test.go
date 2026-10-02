package repoworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type grantFailureRunner struct {
	calls int
	fail  bool
}

func (r *grantFailureRunner) Publish(context.Context, []string, string) error {
	r.calls++
	if r.fail {
		return errors.New("synthetic publication failure")
	}
	return nil
}

func TestGrantReductionBarrierAndPublicationRetry(t *testing.T) {
	for _, operation := range []string{"downgrade", "revoke", "regrant_read"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			owner, guest, repo := uuid.NewString(), uuid.NewString(), uuid.NewString()
			if err := os.MkdirAll(filepath.Join(root, "admin", "clients"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := atomicJSON(filepath.Join(root, "admin", "repositories", repo+".json"), repositoryRecord{Schema: RepositorySchema, RepoID: repo, OwnerRealmID: owner, State: "active", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := atomicJSON(filepath.Join(root, "admin", "realms", guest+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: guest, State: "active", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			path, _ := realmGrantPath(root, repo, guest)
			original := RealmGrantRecord{Schema: RealmGrantSchema, RepoID: repo, OwnerRealmID: owner, RecipientRealmID: guest, State: "active", Access: "rw", PathOwnerPolicy: "first_committer", CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if operation == "regrant_read" {
				// The canonical revoke may have been written while its publication
				// failed, leaving the previous rw authz effective.
				original.State, original.Access, original.PathOwnerPolicy = "revoked", "", ""
			}
			if err := atomicJSON(path, original); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			runner := &grantFailureRunner{}
			p := ServicePublisher{ServiceWC: root, DataAuthzFile: filepath.Join(root, "authz"), Runner: runner}
			run := func() error {
				if operation == "revoke" {
					_, err := p.RevokeGrant(t.Context(), owner, guest, repo)
					return err
				}
				_, err := p.Grant(t.Context(), owner, guest, repo, "r")
				return err
			}
			if err := run(); err == nil {
				t.Fatal("missing session barrier accepted")
			}
			p.WithRealmSessionsStopped = func(context.Context, string, func() error) error { return context.DeadlineExceeded }
			if err := run(); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || runner.calls != 0 {
				t.Fatal("grant changed before sessions acknowledged")
			}
			barriers := 0
			p.WithRealmSessionsStopped = func(_ context.Context, realm string, publish func() error) error {
				if realm != guest {
					t.Fatal("wrong realm")
				}
				barriers++
				return publish()
			}
			runner.fail = true
			if err := run(); err == nil {
				t.Fatal("publication failure hidden")
			}
			runner.fail = false
			if err := run(); err != nil {
				t.Fatal(err)
			}
			if barriers != 2 || runner.calls != 2 {
				t.Fatalf("retry bypassed barrier or publication: barriers=%d calls=%d", barriers, runner.calls)
			}
		})
	}
}
