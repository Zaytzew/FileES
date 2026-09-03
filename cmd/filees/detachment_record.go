package main

import (
	"path/filepath"
	"time"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/detachment"
	"filees/pkg/localrepo"
)

func defaultDetachmentPath() string {
	return filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "server-detachments.json")
}

// detachmentSource adapts the durable store to the read-only shape ipcserver
// pulls from. Expiry happens inside List, so a daemon left running for a week
// never serves a row that has outlived its lifetime.
type detachmentSource struct{ store *detachment.Store }

func (s detachmentSource) List() []contract.Detachment {
	if s.store == nil {
		return nil
	}
	records := s.store.List()
	out := make([]contract.Detachment, 0, len(records))
	for _, rec := range records {
		item := contract.Detachment{
			ServerID:      rec.ServerID,
			DisplayName:   rec.Name(),
			Address:       rec.Address,
			Cause:         string(rec.Cause),
			At:            rec.At.UTC().Format(time.RFC3339),
			WorkingCopies: rec.WorkingCopies,
		}
		if !rec.Current() {
			item.ReattachedAt = rec.ReattachedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}
	return out
}

// workingCopiesOf collects the local paths that belong to serverID.
//
// It must be called BEFORE the detachment runs. Detaching walks every attached
// record through BeginDetach, and a revoked client stops receiving the view
// that names them; either way, asking afterwards answers a different question
// than the one the reader has, which is where their files went.
//
// Deleted repositories are skipped: their working copy is gone, so offering
// the path would send someone to an empty place.
func workingCopiesOf(store *localrepo.Store, serverID string) []string {
	if store == nil {
		return nil
	}
	seen := make(map[string]bool)
	paths := make([]string, 0, 4)
	for _, record := range store.List() {
		if record.ServerID != serverID || record.State == localrepo.StateDeleted {
			continue
		}
		path := record.LocalPath
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}
