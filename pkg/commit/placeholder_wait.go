package commit

import (
	"filees/pkg/errcat"
	"filees/pkg/errmap"
)

// Attached to the existing pending item, not a separate unbounded path cache.
// A restart may remind the person once; hydration/removal retires the episode.
func (s *Service) markPlaceholderWait(rel string, pending []pendingEntry, waiting bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pe := range pending {
		if pe.item.Rel != rel {
			continue
		}
		cur := s.staging[rel]
		if cur != pe.item || cur.ver != pe.ver {
			return false
		}
		changed := waiting && !cur.placeholderWaitReported
		cur.placeholderWaitReported = waiting
		return changed
	}
	return false
}

func (s *Service) reportForeignPlaceholderWait(rel string, pending []pendingEntry) {
	if !s.markPlaceholderWait(rel, pending, true) {
		return
	}
	s.Logger.Warnf("defer add %s (another cloud provider keeps it off this disk)", rel)
	entry := errmap.Classify(errcat.New(errcat.KeyForeignPlaceholderWait, nil, nil))
	entry.Details = rel
	s.ErrSink.Emit(entry)
}
