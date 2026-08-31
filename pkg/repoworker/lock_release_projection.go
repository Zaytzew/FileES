package repoworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"filees/pkg/clientview"
)

// PublishLockRelease writes the same server-owned record into the private
// views of the requester and current holder. It also removes a previous holder
// projection after a same-token migration. No record is broadcast to another
// client or carried through the generic notice channel.
func (p ServicePublisher) PublishLockRelease(ctx context.Context, record LockReleaseRecord) error {
	if !filepath.IsAbs(p.ServiceWC) || p.Runner == nil {
		return errors.New("lock release projector is incomplete")
	}
	if err := record.Validate(); err != nil {
		return err
	}
	clientsRoot := filepath.Join(filepath.Clean(p.ServiceWC), "clients")
	entries, err := os.ReadDir(clientsRoot)
	if err != nil {
		return err
	}
	type clientProjection struct {
		path string
		view clientview.View
	}
	clients := make([]clientProjection, 0, len(entries))
	aliases := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		viewPath := filepath.Join(clientsRoot, entry.Name(), "view.json")
		view, err := clientview.Load(viewPath)
		if err != nil {
			continue
		}
		if previous, exists := aliases[view.RealmID]; exists && previous != view.RealmAlias && previous != "" && view.RealmAlias != "" {
			return errors.New("lock release projection found conflicting realm aliases")
		}
		if view.RealmAlias != "" {
			aliases[view.RealmID] = view.RealmAlias
		}
		clients = append(clients, clientProjection{path: viewPath, view: view})
	}
	foundRequester, foundHolder := false, false
	for _, projection := range clients {
		foundRequester = foundRequester || projection.view.ClientID == record.RequesterClientID
		foundHolder = foundHolder || projection.view.ClientID == record.HolderClientID
	}
	if !foundRequester || !foundHolder {
		return errors.New("lock release projection target is missing")
	}
	commitPaths := make([]string, 0, 2)
	for i := range clients {
		projection := &clients[i]
		role, alias := "", ""
		switch projection.view.ClientID {
		case record.RequesterClientID:
			role, alias = "requester", aliases[record.HolderRealmID]
		case record.HolderClientID:
			role, alias = "holder", aliases[record.RequesterRealmID]
		}
		wanted := clientview.LockReleaseRequest{
			RequestID: record.RequestID, RepoID: record.RepoID, Path: record.Path,
			ObservedLockID: record.ObservedLockID, Role: role, CounterpartyRealmAlias: alias,
			State: string(record.State), CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
		}
		changed := false
		kept := projection.view.LockReleaseRequests[:0]
		found := false
		for _, current := range projection.view.LockReleaseRequests {
			if current.RequestID != record.RequestID {
				kept = append(kept, current)
				continue
			}
			found = true
			if role != "" {
				kept = append(kept, wanted)
				changed = current != wanted
			} else {
				changed = true
			}
		}
		if role != "" && !found {
			kept = append(kept, wanted)
			changed = true
		}
		if !changed {
			if role != "" {
				commitPaths = append(commitPaths, projection.path)
			}
			continue
		}
		projection.view.LockReleaseRequests = kept
		sort.Slice(projection.view.LockReleaseRequests, func(i, j int) bool {
			left, right := projection.view.LockReleaseRequests[i], projection.view.LockReleaseRequests[j]
			if left.CreatedAt.Equal(right.CreatedAt) {
				return left.RequestID < right.RequestID
			}
			return left.CreatedAt.Before(right.CreatedAt)
		})
		projection.view.Generation++
		projection.view.GeneratedAt = p.now()
		if _, err := clientview.StoreIfNewer(projection.path, projection.view); err != nil {
			return err
		}
		commitPaths = append(commitPaths, projection.path)
	}
	return p.Runner.Publish(ctx, commitPaths, "filees: project lock release "+record.RequestID)
}
