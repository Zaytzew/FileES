package commit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"filees/pkg/talk"
)

func TestPollReconcilesOwnerAccessOnlyAfterSuccessfulObservation(t *testing.T) {
	failed := errors.New("observation failed")
	for _, tc := range []struct {
		name, realm, owner string
		cli                revisionClient
		want               int
	}{
		{"unchanged HEAD", "owner", "owner", revisionClient{remote: 7, local: 7}, 1},
		{"updated", "owner", "owner", revisionClient{remote: 8, local: 7}, 1},
		{"guest", "guest", "owner", revisionClient{remote: 7, local: 7}, 0},
		{"unknown realm", "", "owner", revisionClient{remote: 7, local: 7}, 0},
		{"unknown owner", "owner", "", revisionClient{remote: 7, local: 7}, 0},
		{"no identities", "", "", revisionClient{remote: 7, local: 7}, 0},
		{"HEAD failure", "owner", "owner", revisionClient{remoteErr: failed}, 0},
		{"local failure", "owner", "owner", revisionClient{localErr: failed}, 0},
		{"update failure", "owner", "owner", revisionClient{remote: 8, local: 7, updateErr: failed}, 0},
		{"status failure", "owner", "owner", revisionClient{remote: 8, local: 7, statusErr: failed}, 0},
		{"missing file", "owner", "owner", revisionClient{remote: 8, local: 7, status: []client.StatusEntry{{Path: "gone", Item: "missing"}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wc := t.TempDir()
			calls := 0
			s := &Service{Cli: &tc.cli, RepoURL: "svn://example/repo", Logger: talk.With("autolock-test"), RealmID: tc.realm, OwnerRealmID: tc.owner}
			s.AutoUnlockOwned = func(_ context.Context, gotWC, gotRealm string) error {
				calls++
				if gotWC != wc || gotRealm != tc.realm {
					t.Fatalf("wrong scope %q %q", gotWC, gotRealm)
				}
				return nil
			}
			s.pollOnce(t.Context(), wc, filepath.Join(wc, "head.rev"))
			if calls != tc.want {
				t.Fatalf("RW reconciliations = %d, want %d", calls, tc.want)
			}
		})
	}
}

func TestReconcileOwnedAccessWithoutPassportPolicyIsInert(t *testing.T) {
	s := &Service{RealmID: "owner", OwnerRealmID: "owner"}
	s.ReconcileOwnedAccess(t.Context(), t.TempDir())
}
