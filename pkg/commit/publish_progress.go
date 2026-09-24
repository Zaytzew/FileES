package commit

import (
	"os"
	"sync"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

// publishProgressInterval bounds how often a running publication is reported:
// each report becomes an IPC event and an interface refresh.
const publishProgressInterval = 500 * time.Millisecond

// startPublishProgress measures the batch about to be sent and answers the
// callback the native commit reports through (client.WithCommitProgress) and
// the function that ends the report. The totals are the queued batch: files
// that carry content (added, modified, renamed - never directories or
// deletions) and their sizes on disk now. A first publication of gigabytes
// runs as one commit, and before 2026-09-24 the interface could only say "in
// progress" for all of it (Fedora acceptance).
func (s *Service) startPublishProgress(pending []pendingEntry, selected map[string]bool) (func(client.CommitProgress), func()) {
	if s.OnPublishProgress == nil {
		return nil, func() {}
	}
	var files int
	var bytes int64
	for _, entry := range pending {
		item := entry.item
		if !selected[item.Rel] || item.IsDir || item.Op == watcher.Deleted || item.Op == watcher.RenameUncertain {
			continue
		}
		files++
		if info, err := os.Stat(item.Abs); err == nil && info.Mode().IsRegular() {
			bytes += info.Size()
		}
	}
	base := contract.PublishProgress{FilesTotal: files, BytesTotal: bytes, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	s.OnPublishProgress(&base)
	var mu sync.Mutex
	var last time.Time
	report := func(progress client.CommitProgress) {
		mu.Lock()
		defer mu.Unlock()
		done := min(progress.FilesDone, files)
		if done < files && time.Since(last) < publishProgressInterval {
			return
		}
		last = time.Now()
		current := base
		current.FilesDone = done
		current.BytesSent = min(progress.BytesSent, bytes)
		s.OnPublishProgress(&current)
	}
	return report, func() { s.OnPublishProgress(nil) }
}
