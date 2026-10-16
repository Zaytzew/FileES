package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
)

// Each page is an authority-fenced live read. It does NOT trust a stored
// projection or infer unchanged locks from HEAD. Network output happens after
// releasing the lock; no lock spans requests/pages or waits for a client.
func reservationBatch(configPath string, config serverconfig.Config, clientID string, req reservationv1.BatchRequest, stateRoot string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return reservationBatchWith(ctx, config, clientID, req, func(ctx context.Context, id string) (reservationv1.Result, error) {
		selector := reservationv1.Request{Schema: reservationv1.AutolockSchema, RepoID: id, IncludeStorageWrite: true}
		r, err := refreshAutolockProjection(ctx, config, clientID, selector, stateRoot)
		if err == nil {
			attachStorageWrite(configPath, config, selector, &r)
		}
		return r, err
	})
}

func reservationBatchWith(ctx context.Context, config serverconfig.Config, clientID string, req reservationv1.BatchRequest, read func(context.Context, string) (reservationv1.Result, error)) ([]byte, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var raw []byte
	err := repoworker.WithFileLockContext(ctx, filepath.Join(config.Activation.Root, ".service-wc.lock"), func() error {
		view, _, err := authorizedStateView(config.Activation.ServiceWorkingCopy, clientID, reservationv1.Request{Schema: reservationv1.StateSchema})
		if err != nil {
			return err
		}
		if reservationv1.BatchScope(config.ServerID, view) != req.Scope {
			return errors.New("reservation batch authority changed; refresh client view")
		}
		ids := reservationv1.BatchInventory(view)
		page, next, err := reservationv1.BatchPage(ids, req.After)
		if err != nil {
			return err
		}
		r := reservationv1.BatchResponse{Schema: reservationv1.BatchSchema, ServerID: config.ServerID, ClientID: clientID, RealmID: view.RealmID, Scope: req.Scope, After: req.After, Next: next, Repositories: ids, Entries: []reservationv1.BatchEntry{}}
		for _, id := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, err := read(ctx, id)
			if err != nil {
				return err
			} // no partial ACK or stale fallback on authority failure
			if result.RepoID != id || result.Schema != reservationv1.AutolockSchema || result.RepositoryState != "active" {
				return errors.New("invalid reservation batch source")
			}
			sourceRaw, err := json.Marshal(result)
			if err != nil {
				return err
			}
			if len(sourceRaw) > 8<<20 {
				return errors.New("reservation batch item exceeds limit")
			}
			if _, err = reservationv1.ParseResult(sourceRaw); err != nil {
				return err
			}
			r.Entries = append(r.Entries, reservationv1.NewBatchEntry(req.Scope, result, req.Known[id]))
		}
		// Network output is bounded and happens only after the fence is released.
		raw, err = json.Marshal(r)
		if err != nil {
			return err
		}
		if len(raw) > reservationv1.BatchMaxResponseBytes {
			return errors.New("reservation batch response exceeds limit")
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return raw, err
}
