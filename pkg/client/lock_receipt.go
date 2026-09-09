package client

import (
	"context"
	"encoding/xml"
	"errors"
)

// LockReceiptReader proves local possession AND the current repository token.
// LockInfo alone cannot prove that this working copy received a lock response.
type LockReceiptReader interface {
	ConfirmLock(context.Context, string, string, string) (*LockInfo, error)
}

func (c *execClient) ConfirmLock(ctx context.Context, wc, path, comment string) (*LockInfo, error) {
	if nativeWCOps(c) {
		observation, err := c.nativeReadLockObservation(ctx, wc, path)
		if err != nil {
			return nil, err
		}
		local, remote := observation.Local, observation.Remote
		if comment == "" || local == nil || remote == nil || local.Token != remote.Token || local.Owner != remote.Owner || local.Comment != comment || remote.Comment != comment {
			return nil, nil
		}
		return remote, nil
	}
	args := append([]string{"status", "--xml", "--verbose", "--show-updates", "--depth", "empty"}, c.pathArgs(wc, []string{path})...)
	out, err := c.run(ctx, wc, args)
	if err != nil {
		return nil, err
	}
	return parseLockReceipt(out, comment)
}

func parseLockReceipt(raw, comment string) (*LockInfo, error) {
	var doc struct {
		Targets []struct {
			Entries []struct {
				WC struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"wc-status"`
				Repos struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"repos-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	if len(doc.Targets) != 1 || len(doc.Targets[0].Entries) != 1 {
		return nil, errors.New("lock receipt requires exactly one status entry")
	}
	e := doc.Targets[0].Entries[0]
	local, remote := e.WC.Lock, e.Repos.Lock
	if comment == "" || local == nil || remote == nil || remote.Token == "" || local.Token != remote.Token || local.Owner != remote.Owner || local.Comment != comment || remote.Comment != comment {
		return nil, nil // Absence is not proof; never fall back to a local-only lock.
	}
	return &LockInfo{Token: remote.Token, Owner: remote.Owner, Comment: remote.Comment}, nil
}
