//go:build linux || openbsd

package servertool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/storagewatch"
	"filees/internal/svnurl"
	"filees/pkg/clientview"
	"filees/pkg/pathownership"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationprojection"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestStateObservationFenceInvalidatesSuccessFailureAndInterruptedWriter(t *testing.T) {
	gate := storagewatch.Gate{Root: t.TempDir()}
	for _, outcome := range []string{"success", "partial-error", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			confirm, err := stateObservationFence(gate.Root)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() { _ = recover() }()
				_ = gate.WithWriter(t.Context(), func() error {
					if outcome == "interrupted" {
						panic("killed writer")
					}
					if outcome == "partial-error" {
						return errors.New("after mutation")
					}
					return nil
				})
			}()
			if err := confirm(); !errors.Is(err, errStateObservationChanged) {
				t.Fatal("old observation confirmed", err)
			}
		})
	}
	// An orphan does not manufacture a clean/cache-only ACK. A new live source
	// reconciliation may proceed, without clearing that orphan or extending TTL.
	if _, clean, err := gate.ObservationStamp(); err != nil || clean {
		t.Fatal(clean, err)
	}
	confirm, err := stateObservationFence(gate.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(); err != nil {
		t.Fatal(err)
	}
}

func TestReservationBatchRejectsMutationBetweenItems(t *testing.T) {
	c := serverconfig.Config{ServerID: "lab"}
	c.Activation.Root, c.Activation.ServiceWorkingCopy = t.TempDir(), t.TempDir()
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Lab", ClientID: uuid.NewString(), RealmID: uuid.NewString(), ClientRole: "normal", Generation: 1, GeneratedAt: time.Now()}
	for i := 0; i < 2; i++ {
		id := uuid.NewString()
		v.Repositories = append(v.Repositories, clientview.Repository{RepoID: id, State: "active", Access: "rw", DisplayName: id, URL: "svn+ssh://_filees-data@host/" + id, OwnerRealmID: v.RealmID})
	}
	if _, err := clientview.StoreIfNewer(filepath.Join(c.Activation.ServiceWorkingCopy, "clients", v.ClientID, "view.json"), v); err != nil {
		t.Fatal(err)
	}
	req := reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: reservationv1.BatchScope(c.ServerID, v)}
	reads := 0
	raw, err := reservationBatchWith(t.Context(), c, v.ClientID, req, func(ctx context.Context, id string) (reservationv1.Result, error) {
		reads++
		if reads == 2 {
			if err := (storagewatch.Gate{Root: c.Activation.Root}).WithWriter(ctx, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		}
		return reservationv1.Result{Schema: reservationv1.AutolockSchema, RepoID: id, RepositoryState: "active", AsOf: time.Now(), Generation: "1", PathOwnership: &pathownership.Snapshot{RepositoryUUID: id, Revision: 1}}, nil
	})
	if !errors.Is(err, errStateObservationChanged) || len(raw) != 0 {
		t.Fatal("mixed page escaped", err, string(raw))
	}
}

func TestHybridDirectSVNAdminUnlockWithoutFileESMarker(t *testing.T) {
	admin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	run := func(tool string, args ...string) {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	run(admin, "create", repo)
	// svnmucc is not needed: use a directory and a committed file through SVN.
	wc := filepath.Join(root, "wc")
	run(svn, "co", svnurl.File(repo), wc)
	file := filepath.Join(wc, "sample")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", file)
	run(svn, "ci", "--username", "owner", "-m", "fixture", wc)
	gate := storagewatch.Gate{Root: root}
	before, _, err := gate.ObservationStamp()
	if err != nil {
		t.Fatal(err)
	}
	run(svn, "lock", "--username", "owner", "-m", "fixture", file)
	store := reservationprojection.NewStore(filepath.Join(root, "projection"))
	first := refreshReservationProjection(t.Context(), store, svn, admin, root, "repo")
	if first.Unknown || first.Stale || len(first.Reservations) != 1 {
		t.Fatal(first)
	}
	run(admin, "rmlocks", repo, "/sample")
	next := refreshReservationProjection(t.Context(), store, svn, admin, root, "repo")
	if next.Unknown || next.Stale || len(next.Reservations) != 0 || next.Generation == first.Generation {
		t.Fatal("administrator change missed", next)
	}
	after, _, err := gate.ObservationStamp()
	if err != nil || before != after {
		t.Fatal("fixture unexpectedly used FileES journal", err)
	}
}
