package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// TreeMoveSnapshot records the published baseline, NOT current disk bytes.
// The coordinator persists this before requesting the bound remote move. Its
// independent SVN/CFAPI proof must precede ACK; this API never supplies it.
type TreeMoveSnapshot struct {
	Schema, WC, Source, Destination string
	Session                         string
	Sequence                        uint64
	Entries                         []PublicationEntry
}

// Lock order: WC writer -> scanMu -> mu. Callback must not call Scanner. It
// owns persistence and filesystem/provider guards through the exact local
// receipt. Nil is that trusted receipt, never an intent or just a remote ACK.
func (s *Scanner) WithTreeMove(ctx context.Context, source, destination string, operation func(*TreeMoveSnapshot) error) error {
	if operation == nil || !treeMovePaths(source, destination) {
		return errors.New("invalid tree move")
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
	p := &TreeMoveSnapshot{Schema: "filees.scanner-tree-move/v1", WC: s.wc,
		Source: source, Destination: destination, Session: s.session, Sequence: s.sequence}
	for name, e := range base {
		if treeContains(source, name) {
			p.Entries = append(p.Entries, PublicationEntry{Path: name, Present: true,
				Mtime: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir})
		}
	}
	sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].Path < p.Entries[j].Path })
	if err := s.validateTreeMoveSnapshot(p); err != nil {
		return err
	}
	if _, err := treeMoveBaselinePhase(base, p); err != nil {
		return err
	}
	frozen := cloneTreeMoveSnapshot(p)
	if err := operation(p); err != nil {
		return err
	}
	if !sameTreeMoveSnapshot(p, frozen) {
		return errors.New("tree move observations changed inside callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.acknowledgeTreeMoveLocked(frozen)
}

// Recovery consumes the SAME persisted observations, not a new scan. It
// permits only baseline-before or exact baseline-after and never replays the
// remote mutation. Caller revalidates attachment/root/receipt and local effect.
func (s *Scanner) WithTreeMoveRecovery(ctx context.Context, p *TreeMoveSnapshot, inspect func() error) error {
	if inspect == nil {
		return errors.New("missing tree move recovery proof")
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateTreeMoveSnapshot(p); err != nil {
		return err
	}
	frozen := cloneTreeMoveSnapshot(p)
	base, err := s.treeMoveBaseline()
	if err != nil {
		return err
	}
	if _, err := treeMoveBaselinePhase(base, frozen); err != nil {
		return err
	}
	if err := inspect(); err != nil {
		return err
	}
	if !sameTreeMoveSnapshot(p, frozen) {
		return errors.New("tree move recovery observations changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.acknowledgeTreeMoveLocked(frozen)
}

func treeContains(root, name string) bool { return name == root || strings.HasPrefix(name, root+"/") }

func treeMovePaths(source, destination string) bool {
	for _, p := range []string{source, destination} {
		if !publicationPath(p) || !utf8.ValidString(p) || len(p) > 4096 {
			return false
		}
		for _, part := range strings.Split(p, "/") {
			if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.EqualFold(part, ".filees-native-probe") {
				return false
			}
			for _, r := range part {
				if r < 32 || r == 127 {
					return false
				}
			}
		}
	}
	a, b := strings.ToLower(source), strings.ToLower(destination)
	return !treeContains(a, b) && !treeContains(b, a)
}

func (s *Scanner) validateTreeMoveSnapshot(p *TreeMoveSnapshot) error {
	if p == nil || p.Schema != "filees.scanner-tree-move/v1" || p.WC != s.wc ||
		!filepath.IsAbs(p.WC) || filepath.Clean(p.WC) != p.WC || p.Session == "" || p.Sequence == 0 ||
		(p.Session == s.session && p.Sequence > s.sequence) ||
		!treeMovePaths(p.Source, p.Destination) || len(p.Entries) == 0 || len(p.Entries) > 100000 {
		return errors.New("tree move snapshot is unbound or invalid")
	}
	previous, pathBytes, root := "", 0, false
	aliases := make(map[string]bool, len(p.Entries))
	for _, e := range p.Entries {
		pathBytes += len(e.Path)
		alias := strings.ToLower(e.Path)
		if !e.Present || !treeContains(p.Source, e.Path) || !treeMovePaths(e.Path, p.Destination+strings.TrimPrefix(e.Path, p.Source)) || e.Path <= previous ||
			e.Size < 0 || (e.IsDir && (e.Size != 0 || e.MD5 != "")) || pathBytes > 16<<20 || aliases[alias] {
			return errors.New("tree move snapshot has invalid baseline entries")
		}
		aliases[alias] = true
		if e.Path == p.Source {
			root = e.IsDir
		}
		previous = e.Path
	}
	if !root {
		return errors.New("tree move lacks a published directory baseline")
	}
	return nil
}

func cloneTreeMoveSnapshot(p *TreeMoveSnapshot) *TreeMoveSnapshot {
	copy := *p
	copy.Entries = append([]PublicationEntry(nil), p.Entries...)
	return &copy
}

func sameTreeMoveSnapshot(a, b *TreeMoveSnapshot) bool {
	if a == nil || b == nil || a.Schema != b.Schema || a.WC != b.WC || a.Source != b.Source ||
		a.Destination != b.Destination || a.Session != b.Session || a.Sequence != b.Sequence || len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		if a.Entries[i] != b.Entries[i] {
			return false
		}
	}
	return true
}

func (s *Scanner) treeMoveBaseline() (map[string]diskEntry, error) {
	if !s.workingCopyAvailable() {
		return nil, errors.New("tree move WC unavailable")
	}
	f, err := os.Open(s.statePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limited := &io.LimitedReader{R: f, N: (64 << 20) + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	var list []diskEntry
	if err := d.Decode(&list); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF || limited.N == 0 {
		return nil, errors.New("tree move baseline has trailing data")
	}
	base := make(map[string]diskEntry, len(list))
	for _, e := range list {
		if _, exists := base[e.Path]; exists {
			return nil, errors.New("tree move baseline has duplicate paths")
		}
		base[e.Path] = e
	}
	return base, nil
}

// Scoped CAS. No mixed before/after, added source entry, occupied destination
// or case-alias may be silently acknowledged. Unrelated baseline is preserved.
func treeMoveBaselinePhase(base map[string]diskEntry, p *TreeMoveSnapshot) (bool, error) {
	before, after := make(map[string]diskEntry), make(map[string]diskEntry)
	for _, e := range p.Entries {
		de := diskEntry{Path: e.Path, Mtime: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
		before[de.Path] = de
		de.Path = p.Destination + strings.TrimPrefix(e.Path, p.Source)
		after[de.Path] = de
	}
	isBefore, isAfter := true, true
	countBefore, countAfter := 0, 0
	for name, e := range base {
		if treeContains(strings.ToLower(p.Source), strings.ToLower(name)) {
			countBefore++
			isBefore = isBefore && before[name] == e
			isAfter = false
		}
		if treeContains(strings.ToLower(p.Destination), strings.ToLower(name)) {
			countAfter++
			isAfter = isAfter && after[name] == e
			isBefore = false
		}
	}
	if isBefore && countBefore == len(before) && countAfter == 0 {
		return false, nil
	}
	if isAfter && countAfter == len(after) && countBefore == 0 {
		return true, nil
	}
	return false, errors.New("tree move baseline differs from its immutable snapshot")
}

func (s *Scanner) acknowledgeTreeMoveLocked(p *TreeMoveSnapshot) error {
	base, err := s.treeMoveBaseline()
	if err != nil {
		return err
	}
	after, err := treeMoveBaselinePhase(base, p)
	if err != nil {
		return err
	}
	if after {
		return nil
	} // Exact replay never rolls back a later observed edit.
	// Hash work is disposable, not publication evidence. Remove old/target work
	// before publishing the baseline; a failure retains the owner's fence.
	s.mu.Lock()
	kept := s.backlog[:0]
	for _, b := range s.backlog {
		if !treeContains(p.Source, b.Rel) && !treeContains(p.Destination, b.Rel) {
			kept = append(kept, b)
		}
	}
	s.backlog = kept
	for name := range s.pendingMD5 {
		if treeContains(p.Source, name) || treeContains(p.Destination, name) {
			delete(s.pendingMD5, name)
		}
	}
	s.mu.Unlock()
	if err := s.saveBacklog(); err != nil {
		return err
	}
	for _, e := range p.Entries {
		delete(base, e.Path)
		name := p.Destination + strings.TrimPrefix(e.Path, p.Source)
		base[name] = diskEntry{Path: name, Mtime: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
	}
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
	for name := range s.cur {
		if treeContains(p.Source, name) || treeContains(p.Destination, name) {
			delete(s.cur, name)
		}
	}
	for name := range s.missingSince {
		if treeContains(p.Source, name) || treeContains(p.Destination, name) {
			delete(s.missingSince, name)
		}
	}
	for _, e := range p.Entries {
		name := p.Destination + strings.TrimPrefix(e.Path, p.Source)
		s.cur[name] = meta{MtimeSec: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
		if p.Session == s.session {
			for _, path := range []string{e.Path, name} {
				if p.Sequence > s.acknowledged[path] {
					s.acknowledged[path] = p.Sequence
				}
			}
		}
	}
	s.totalBytes = indexBytes(s.cur)
	if p.Session == s.session {
		if s.acknowledgedTrees == nil {
			s.acknowledgedTrees = make(map[string]uint64)
		}
		for _, root := range []string{p.Source, p.Destination} {
			if p.Sequence > s.acknowledgedTrees[root] {
				s.acknowledgedTrees[root] = p.Sequence
			}
		}
	}
	return nil
}
