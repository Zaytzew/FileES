package main

import (
	"context"
	"errors"
	"time"

	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/publicstate"
)

// The protocol mirror is memory-only. Persisted presenter rows never prove
// that an initial authoritative inventory has been received in this process.
func (c *publicShareCache) stateRequest(serverID, realmID string, epoch uint64) (publicstate.Request, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[serverID]
	if c.epochs[serverID] != epoch {
		return (publicstate.Snapshot{}).Request(), c.stateSerial[serverID]
	}
	if s.Binding.RealmID != "" && s.Binding.RealmID != realmID {
		s = publicstate.Snapshot{}
		delete(c.states, serverID)
	}
	return s.Request(), c.stateSerial[serverID]
}

var errPublicLinksSuperseded = errors.New("public link read superseded by mutation or activation")

func (c *publicShareCache) completeState(serverID string, epoch, serial uint64, req publicstate.Request, response publicstate.Response, expected publicstate.Binding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[serverID] != epoch || c.blocked[serverID] || c.stateSerial[serverID] != serial || c.mutating[serverID] {
		return errPublicLinksSuperseded
	}
	next, err := c.states[serverID].Apply(req, response, expected)
	if err != nil {
		return err
	}
	rows := make([]contract.PublicShareSummary, 0, len(next.Items))
	for _, item := range next.Items {
		row := publicShareSummaryFromControl(item.Share)
		row.ServerID, row.RepoDisplayName = serverID, item.RepoDisplayName
		rows = append(rows, row)
	}
	c.states[serverID] = next
	c.setPartialLocked(serverID, rows, nil)
	return nil
}

// Serialize only own mutations, never hold a network read in front of the
// user's command. Reads may run concurrently but cannot publish across this
// barrier. The lease is process-local and always released, also on lost ACK.
func (c *publicShareCache) beginMutation(ctx context.Context, serverID string) (uint64, func(), error) {
	c.mu.Lock()
	gate := c.mutationGates[serverID]
	if gate == nil {
		gate = make(chan struct{}, 1)
		c.mutationGates[serverID] = gate
	}
	c.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-gate
		return 0, nil, err
	}
	c.mu.Lock()
	c.stateSerial[serverID]++
	c.mutating[serverID] = true
	epoch := c.epochs[serverID]
	c.mu.Unlock()
	return epoch, func() {
		c.mu.Lock()
		c.stateSerial[serverID]++
		delete(c.mutating, serverID)
		c.mu.Unlock()
		<-gate
	}, nil
}

// A receipt changes only its item. It does NOT refresh the age/completeness
// of the inventory or sibling rows, and cannot authorize any operation.
func (c *publicShareCache) applyReceipt(serverID, clientID string, epoch uint64, result control.PublicShareResult) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := result.StateReceipt
	if r == nil || c.epochs[serverID] != epoch || c.blocked[serverID] || !c.mutating[serverID] || c.bindings[serverID].ClientID != clientID {
		return false
	}
	expected := publicstate.Binding{ServerID: serverID, ClientID: clientID, RealmID: r.RealmID}
	s := c.states[serverID]
	if s.Binding != (publicstate.Binding{}) && s.Binding != expected {
		return false
	}
	if (result.State == "deleted") != (r.Share == nil) {
		return false
	}
	if r.Share != nil && (r.Share.ChannelID != result.ChannelID || r.Share.RepoID != r.RepoID || r.Share.State != result.State) {
		return false
	}
	s.Binding = expected
	if s.Items == nil {
		s.Items = map[string]publicstate.Item{}
	}
	var row contract.PublicShareSummary
	if r.Share != nil {
		item := publicstate.Item{RepoDisplayName: r.RepoDisplayName, Share: *r.Share}
		s.Items[result.ChannelID] = item
		row = publicShareSummaryFromControl(*r.Share)
		row.ServerID, row.RepoDisplayName = serverID, r.RepoDisplayName
		row.ObservedAt = c.now().UTC().Format(time.RFC3339Nano)
	} else {
		delete(s.Items, result.ChannelID)
	}
	rows := make([]contract.PublicShareSummary, 0, len(c.byServer[serverID])+1)
	for _, old := range c.byServer[serverID] {
		if old.ChannelID != result.ChannelID {
			rows = append(rows, old)
		}
	}
	if r.Share != nil {
		rows = append(rows, row)
	}
	c.byServer[serverID], c.states[serverID] = rows, s
	c.generation++
	c.saveLocked(serverID)
	return true
}
