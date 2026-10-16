package servertool

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/pathownership"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestReservationBatchAuthorityPaginationLiveReadAndFailures(t *testing.T) {
	c := serverconfig.Config{ServerID: "lab"}
	c.Activation.Root = t.TempDir()
	c.Activation.ServiceWorkingCopy = t.TempDir()
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Lab", ClientID: uuid.NewString(), RealmID: uuid.NewString(), Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal"}
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		v.Repositories = append(v.Repositories, clientview.Repository{RepoID: id, State: "active", Access: "rw", DisplayName: id, URL: "svn+ssh://_filees-data@host/" + id, OwnerRealmID: v.RealmID})
	}
	viewPath := filepath.Join(c.Activation.ServiceWorkingCopy, "clients", v.ClientID, "view.json")
	if _, err := clientview.StoreIfNewer(viewPath, v); err != nil {
		t.Fatal(err)
	}
	req := reservationv1.BatchRequest{Schema: reservationv1.BatchSchema, Scope: reservationv1.BatchScope(c.ServerID, v), Known: map[string]string{}}
	reads := 0
	read := func(ctx context.Context, id string) (reservationv1.Result, error) {
		reads++
		return reservationv1.Result{Schema: reservationv1.AutolockSchema, RepoID: id, RepositoryState: "active", AsOf: time.Now(), Generation: "1", ViewGeneration: v.Generation, ViewGeneratedAt: &v.GeneratedAt, PathOwnership: &pathownership.Snapshot{RepositoryUUID: id, Revision: 1}}, ctx.Err()
	}
	cache := map[string]reservationv1.Result{}
	for pass := 0; pass < 2; pass++ {
		req.After = ""
		pages := 0
		for {
			raw, err := reservationBatchWith(t.Context(), c, v.ClientID, req, read)
			if err != nil {
				t.Fatal(err)
			}
			response, err := reservationv1.ParseBatchResponse(raw)
			if err != nil {
				t.Fatal(err)
			}
			merged, err := reservationv1.ApplyBatch(req, response, c.ServerID, v.ClientID, v.RealmID, cache)
			if err != nil {
				t.Fatal(err)
			}
			if pass == 1 {
				for _, e := range response.Entries {
					if e.Result != nil {
						t.Fatal("unchanged content resent")
					}
				}
			}
			for id, result := range merged {
				cache[id] = result
				req.Known[id] = reservationv1.BatchVersion(req.Scope, result)
			}
			pages++
			if response.Next == "" {
				break
			}
			req.After = response.Next
		}
		if pages != 3 || reads != 9*(pass+1) {
			t.Fatal("source skipped or pages repeated", pages, reads)
		}
	}
	req.After = ""
	failedReads := 0
	raw, err := reservationBatchWith(t.Context(), c, v.ClientID, req, func(ctx context.Context, id string) (reservationv1.Result, error) {
		failedReads++
		if failedReads == 2 {
			return reservationv1.Result{}, errors.New("read failed")
		}
		return read(ctx, id)
	})
	if err == nil || len(raw) != 0 {
		t.Fatal("partial success escaped")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reservationBatchWith(ctx, c, v.ClientID, req, read); err == nil {
		t.Fatal("cancel accepted")
	}
	if _, err := reservationBatchWith(t.Context(), c, uuid.NewString(), req, read); err == nil {
		t.Fatal("unknown actor accepted")
	}
	// Access changes invalidate the cursor even if the selected page IDs survive.
	v.Generation++
	v.Repositories[0].Access = "r"
	v.GeneratedAt = time.Now().UTC()
	if _, err := clientview.StoreIfNewer(viewPath, v); err != nil {
		t.Fatal(err)
	}
	before := reads
	if _, err := reservationBatchWith(t.Context(), c, v.ClientID, req, read); err == nil || reads != before {
		t.Fatal("old scope read authority")
	}
}
