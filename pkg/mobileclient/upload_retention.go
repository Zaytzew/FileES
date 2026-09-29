package mobileclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const deliveredRetention = 7 * 24 * time.Hour

func uploadComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\:")
}

// AcknowledgeUploadSources is local, not a server ACK. The caller must first
// durably mark ALL Sources as seen in the matching server/repository scope.
// A crash before this call retains the receipt, even after a long offline stay.
func (s Store) AcknowledgeUploadSources(ctx context.Context, repoID, id string) error {
	if !uploadComponent(repoID) || !uploadComponent(id) {
		return errors.New("invalid upload identity")
	}
	unlock, err := s.lockQueue(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	item, err := s.loadUploadMeta(repoID, id)
	if err != nil {
		return err
	}
	if item.RepoID != repoID || item.ID != id || !delivered(item.State) {
		return errors.New("upload is not a matching delivered receipt")
	}
	if !item.SourcesAcknowledgedAt.IsZero() {
		return nil
	}
	item.SourcesAcknowledgedAt = time.Now().UTC()
	return s.recordUploadOutcome(item)
}

func delivered(state UploadState) bool {
	return state == UploadCommitted || state == UploadDroppedSame
}

// PruneDeliveredUploads removes only old, safely consumed receipts. It does
// not scan other repositories, pending intents, or source files on the phone.
func (s Store) PruneDeliveredUploads(ctx context.Context, repoID string) error {
	if !uploadComponent(repoID) {
		return errors.New("invalid upload repo_id")
	}
	unlock, err := s.lockQueue(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	items, err := s.ListUploads(repoID)
	if err != nil {
		return err
	}
	return s.pruneDeliveredUploads(repoID, items, time.Now().UTC())
}

// Called under the queue lock. Unknown/zero/future timestamps fail closed.
func (s Store) pruneDeliveredUploads(repoID string, items []PendingUpload, now time.Time) error {
	for _, item := range items {
		if item.RepoID != repoID || !uploadComponent(item.ID) || !delivered(item.State) {
			continue
		}
		last := item.EnqueuedAt
		if last.IsZero() {
			continue
		}
		if item.LastAttemptAt.After(last) {
			last = item.LastAttemptAt
		}
		if len(item.Sources) > 0 && item.SourcesAcknowledgedAt.IsZero() {
			continue
		}
		if item.SourcesAcknowledgedAt.After(last) {
			last = item.SourcesAcknowledgedAt
		}
		if now.Sub(last) < deliveredRetention {
			continue
		}
		// A prior successful receipt write may have been followed by a failed
		// payload removal. Finish that first; never orphan a remaining payload.
		if err := os.Remove(s.uploadPayloadPath(repoID, item.ID)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(s.uploadMetaPath(repoID, item.ID)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
