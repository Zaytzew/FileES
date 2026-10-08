//go:build !nocfapi

package main

import (
	"context"
	"time"

	"filees/internal/anchorrepair"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

const (
	anchorDamageSample = 5
	// A damaged folder is looked at again soon, so that a repair shows without
	// a restart; a healthy one rarely, because looking opens every entry.
	anchorDamageRecheck = 30 * time.Second
	anchorDamageRoutine = 15 * time.Minute
)

func anchorDamageOf(found []anchorrepair.Damaged) *contract.AnchorDamage {
	if len(found) == 0 {
		return nil
	}
	damage := &contract.AnchorDamage{Entries: len(found)}
	for _, entry := range found {
		if len(damage.Sample) == anchorDamageSample {
			break
		}
		damage.Sample = append(damage.Sample, entry.Path)
	}
	return damage
}

// inspectDamage looks for entries of the virtual folder whose cloud metadata
// Windows cannot read and remembers the answer for the GUI. The daemon only
// reports them: removing one needs the cloud files filter detached from the
// volume, which takes administrator rights and the person's consent
// (internal/anchorrepair). A folder that cannot be inspected keeps its last
// answer.
func (m *anchorManager) inspectDamage(record localrepo.Record) int {
	key := anchorKey(record)
	found, err := anchorDamageScan(record.LocalPath)
	m.mu.Lock()
	previous := m.damage[key]
	if err != nil {
		m.mu.Unlock()
		if previous == nil {
			return 0
		}
		return previous.Entries
	}
	current := anchorDamageOf(found)
	if m.damage == nil {
		m.damage = make(map[string]*contract.AnchorDamage)
	}
	if current == nil {
		delete(m.damage, key)
	} else {
		m.damage[key] = current
	}
	m.mu.Unlock()
	log := talk.With("anchor:" + record.ServerID + "/" + record.RepoID)
	switch {
	case current != nil && (previous == nil || previous.Entries != current.Entries):
		log.Warnf("%d entries at %s have cloud metadata Windows cannot read; removing them needs administrator rights", current.Entries, record.LocalPath)
	case current == nil && previous != nil:
		log.Infof("no unreadable entries remain at %s", record.LocalPath)
	}
	// The GUI redraws a repository only when told its state changed; without
	// this the card kept the answer from before the first inspection.
	changed := (current == nil) != (previous == nil) || current != nil && previous != nil && current.Entries != previous.Entries
	if m.publishDamage(record) && current != nil {
		changed = true // known before, but nobody could read it yet
	}
	if changed && m.guardChanged != nil {
		m.guardChanged(record.RepoID)
	}
	if current == nil {
		return 0
	}
	return current.Entries
}

func (m *anchorManager) damageOf(key string) *contract.AnchorDamage {
	m.mu.Lock()
	defer m.mu.Unlock()
	damage := m.damage[key]
	if damage == nil {
		return nil
	}
	return &contract.AnchorDamage{Entries: damage.Entries, Sample: append([]string(nil), damage.Sample...)}
}

// publishDamage hands the repository's state the place to read the damage
// from, and reports whether this call was the first to do so. At daemon start
// an anchor connects before its repository state exists, so one attempt at
// connect time is not enough: every inspection tries again.
func (m *anchorManager) publishDamage(record localrepo.Record) bool {
	if m.repos == nil {
		return false
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return false
	}
	key := anchorKey(record)
	repo.SetAnchorDamageSource(func() *contract.AnchorDamage { return m.damageOf(key) })
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.damagePublished[key] == repo {
		return false
	}
	if m.damagePublished == nil {
		m.damagePublished = make(map[string]*ipcserver.RepoState)
	}
	m.damagePublished[key] = repo
	return true
}

// watchAnchorDamage inspects now and then again: soon while something is
// damaged, rarely while nothing is.
func watchAnchorDamage(ctx context.Context, damaged, healthy time.Duration, inspect func() int) {
	for {
		wait := healthy
		if inspect() > 0 {
			wait = damaged
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
