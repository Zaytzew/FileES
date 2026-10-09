package watcher

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// FileDeleteSnapshot is the published baseline of exactly one file. Absence
// here is not deletion intent: only the coordinator's durable OS completion
// and independent remote receipt can authorize the later scoped ACK.
type FileDeleteSnapshot struct {
	Schema, WC, Source, Session string
	Sequence                    uint64
	Entry                       PublicationEntry
}

// Lock order: real WC writer -> scanMu -> mu. Prepare captures observations
// but never acknowledges them, even if the callback succeeds (ACK_DELETE is
// permission, not evidence that Windows deleted the object).
func (s *Scanner) WithFileDeletePreparation(ctx context.Context, source string, prepare func(*FileDeleteSnapshot) error) error {
	if prepare == nil || !fileDeletePath(source) {
		return errors.New("invalid file deletion preparation")
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := s.treeMoveBaseline()
	if err != nil {
		return err
	}
	p := &FileDeleteSnapshot{Schema: "filees.scanner-file-delete/v1", WC: s.wc, Source: source,
		Session: s.session, Sequence: s.sequence, Entry: PublicationEntry{Path: source}}
	if e, ok := base[source]; ok {
		p.Entry = PublicationEntry{Path: source, Present: true, Mtime: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
	}
	if err := s.validateFileDeleteSnapshot(p); err != nil {
		return err
	}
	if _, err := fileDeleteBaselinePhase(base, p); err != nil {
		return err
	}
	frozen := *p
	if err := prepare(p); err != nil {
		return err
	}
	if *p != frozen {
		return errors.New("file deletion observations changed during preparation")
	}
	return ctx.Err()
}

// Recovery consumes persisted observations, never a new scan or inference
// from disk absence. inspect must independently prove the complete operation.
func (s *Scanner) WithFileDeleteRecovery(ctx context.Context, p *FileDeleteSnapshot, inspect func() error) error {
	if inspect == nil {
		return errors.New("missing file deletion completion proof")
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateFileDeleteSnapshot(p); err != nil {
		return err
	}
	frozen := *p
	base, err := s.treeMoveBaseline()
	if err != nil {
		return err
	}
	if _, err := fileDeleteBaselinePhase(base, &frozen); err != nil {
		return err
	}
	if err := inspect(); err != nil {
		return err
	}
	if *p != frozen {
		return errors.New("file deletion recovery observations changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.acknowledgeFileDeleteLocked(&frozen)
}

func fileDeletePath(rel string) bool {
	if !publicationPath(rel) || !utf8.ValidString(rel) || len(rel) > 4096 {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.EqualFold(part, ".filees-native-probe") {
			return false
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return false
			}
		}
	}
	return true
}

func ValidateFileDeleteSnapshot(p *FileDeleteSnapshot, wc string) error {
	if p == nil || p.Schema != "filees.scanner-file-delete/v1" || p.WC != wc || !filepath.IsAbs(wc) || filepath.Clean(wc) != wc || len(wc) > 32768 ||
		p.Session == "" || len(p.Session) > 128 || !utf8.ValidString(p.Session) || p.Sequence == 0 || !fileDeletePath(p.Source) || p.Entry.Path != p.Source ||
		p.Entry.IsDir || p.Entry.Size < 0 || len(p.Entry.Identity) > 1024 || !utf8.ValidString(p.Entry.Identity) || len(p.Entry.MD5) > 128 {
		return errors.New("file deletion snapshot is unbound or invalid")
	}
	if !p.Entry.Present && (p.Entry.Mtime != 0 || p.Entry.Size != 0 || p.Entry.MD5 != "" || p.Entry.Identity != "") {
		return errors.New("absent file deletion baseline contains metadata")
	}
	return nil
}

func (s *Scanner) validateFileDeleteSnapshot(p *FileDeleteSnapshot) error {
	if err := ValidateFileDeleteSnapshot(p, s.wc); err != nil {
		return err
	}
	if p.Session == s.session && p.Sequence > s.sequence {
		return errors.New("file deletion claims a future scanner generation")
	}
	return nil
}

// Only exact before or exact after is accepted. Case aliases, descendants,
// replaced baselines or directory entries are never silently acknowledged.
func fileDeleteBaselinePhase(base map[string]diskEntry, p *FileDeleteSnapshot) (bool, error) {
	for name := range base {
		if name != p.Source && treeContains(strings.ToLower(p.Source), strings.ToLower(name)) {
			return false, errors.New("file deletion baseline has aliases or descendants")
		}
	}
	e, present := base[p.Source]
	if !present {
		return true, nil
	}
	wanted := diskEntry{Path: p.Source, Mtime: p.Entry.Mtime, Size: p.Entry.Size, MD5: p.Entry.MD5, Identity: p.Entry.Identity}
	if !p.Entry.Present || e != wanted {
		return false, errors.New("file deletion baseline changed after preparation")
	}
	return false, nil
}

func (s *Scanner) acknowledgeFileDeleteLocked(p *FileDeleteSnapshot) error {
	base, err := s.treeMoveBaseline()
	if err != nil {
		return err
	}
	after, err := fileDeleteBaselinePhase(base, p)
	if err != nil || after {
		return err
	} // Replay must not roll back later observed work.
	s.mu.Lock()
	kept := s.backlog[:0]
	for _, b := range s.backlog {
		if b.Rel != p.Source {
			kept = append(kept, b)
		}
	}
	s.backlog = kept
	delete(s.pendingMD5, p.Source)
	s.mu.Unlock()
	if err := s.saveBacklog(); err != nil {
		return err
	}
	delete(base, p.Source)
	list := make([]diskEntry, 0, len(base))
	for _, e := range base {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	if err := s.writeJSON(s.statePath, list); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cur, p.Source)
	delete(s.missingSince, p.Source)
	if p.Session == s.session && p.Sequence > s.acknowledged[p.Source] {
		s.acknowledged[p.Source] = p.Sequence
	}
	s.totalBytes = indexBytes(s.cur)
	return nil
}
