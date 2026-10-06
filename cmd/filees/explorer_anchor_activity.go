//go:build !nocfapi

package main

import (
	"filees/pkg/activity"
	"filees/pkg/localrepo"
)

// The same receipt feeds the existing journal and Windows notifications for
// both read-only and writable points. Metadata seeding, denied fetches and
// unsuccessful SVN adoption do not reach this callback.
func (m *anchorManager) recordHydration(record localrepo.Record, identity string, revision int64, size *int64) {
	if m.activity == nil {
		return
	}
	rel, ok := cleanAnchorIdentity(identity)
	if !ok || revision <= 0 {
		return
	}
	changed, err := m.activity.RecordHydration(activity.Entry{
		RepoID: record.RepoID, Path: rel, Kind: activity.Modified,
		Stage: activity.Received, Revision: revision, Size: size,
	})
	if err != nil {
		m.log.Warnf("Explorer download activity: %v", err)
		return // A diagnostic failure cannot turn a completed download into HOLD.
	}
	if changed && m.activityChanged != nil {
		m.activityChanged(record.RepoID)
	}
}
