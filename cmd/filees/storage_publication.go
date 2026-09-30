package main

import (
	"context"
	"filees/pkg/errcat"
	"filees/pkg/reposupervisor"
	"filees/pkg/reservationclient"
)

// No network request here: the existing state lane persists authoritative risk.
// Reads/recovery keep running while only admission of a new publication waits.
func (c *reservationProjectionCoordinator) CheckStorage(ctx context.Context, key reposupervisor.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.profiles[key.ServerID]
	if !ok {
		return errcat.New(errcat.KeyServerStorageHeld, nil, nil)
	}
	blocked, err := reservationclient.StorageBlocked(p.CachePath, key.ServerID, key.RepoID)
	if err == nil {
		err = c.results[key].storageErr
	}
	if blocked || err != nil {
		return errcat.New(errcat.KeyServerStorageHeld, nil, err)
	}
	return nil
}
