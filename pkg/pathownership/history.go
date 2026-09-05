// Package pathownership reconstructs object continuity from authoritative SVN
// revisions. It never infers identity from content or a client-owned property.
package pathownership

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
)

type Change struct {
	Path         string `xml:",chardata"`
	Action       string `xml:"action,attr"`
	Kind         string `xml:"kind,attr"`
	CopyFrom     string `xml:"copyfrom-path,attr"`
	CopyRevision int64  `xml:"copyfrom-rev,attr"`
}
type Revision struct {
	Number  int64    `xml:"revision,attr"`
	Author  string   `xml:"author"`
	Changes []Change `xml:"paths>path"`
}
type Object struct {
	ID              string `json:"object_id"`
	CreatedRevision int64  `json:"created_revision"`
	FirstCommitter  string `json:"first_committer"`
	Kind            string `json:"kind"`
}
type Entry struct {
	Path string `json:"path"`
	Object
	OwnerRealmID string `json:"owner_realm_id,omitempty"`
}
type Snapshot struct {
	Revision int64   `json:"revision"`
	Entries  []Entry `json:"entries"`
}

func ParseLog(raw []byte) ([]Revision, error) {
	var log struct {
		XMLName xml.Name   `xml:"log"`
		Entries []Revision `xml:"logentry"`
	}
	if err := xml.Unmarshal(raw, &log); err != nil {
		return nil, err
	}
	return log.Entries, nil
}

func canonical(value string, allowRoot bool) bool {
	return strings.HasPrefix(value, "/") && path.Clean(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\\") && (allowRoot || value != "/")
}
func under(value, parent string) bool {
	return value == parent || parent == "/" || strings.HasPrefix(value, parent+"/")
}
func clone(input map[string]Object) map[string]Object {
	result := make(map[string]Object, len(input))
	for p, o := range input {
		result[p] = o
	}
	return result
}
func newObject(repo string, revision int64, author, p, kind string) Object {
	// The birth revision and path identify an incarnation. Re-adding a deleted
	// pathname creates a different object, regardless of equal content.
	sum := sha256.Sum256([]byte(repo + "\x00" + strconv.FormatInt(revision, 10) + "\x00" + p))
	return Object{ID: hex.EncodeToString(sum[:]), CreatedRevision: revision, FirstCommitter: author, Kind: kind}
}

// Replay requires a complete repository log from r1 to head. Copy snapshots
// are retained only for revisions actually referenced by copyfrom. A unique
// copy+delete in one revision preserves identity; forks and later deletes do
// not. Ambiguous copies of a deleted source fail closed rather than picking
// an arbitrary successor.
func Replay(ctx context.Context, repo string, head int64, revisions []Revision) (Snapshot, error) {
	if repo == "" || head < 0 {
		return Snapshot{}, errors.New("invalid ownership history scope")
	}
	revisions = append([]Revision(nil), revisions...)
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number < revisions[j].Number })
	if int64(len(revisions)) != head {
		return Snapshot{}, errors.New("incomplete ownership history")
	}
	needed := map[int64]bool{}
	lastUse := map[int64]int64{}
	for i, rev := range revisions {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if rev.Number != int64(i)+1 {
			return Snapshot{}, errors.New("non-contiguous ownership history")
		}
		for _, c := range rev.Changes {
			if !canonical(c.Path, true) || (c.Action != "A" && c.Action != "M" && c.Action != "D" && c.Action != "R") {
				return Snapshot{}, errors.New("invalid SVN ownership change")
			}
			if c.CopyFrom != "" {
				if !canonical(c.CopyFrom, true) || c.CopyRevision < 0 || c.CopyRevision >= rev.Number || (c.Action != "A" && c.Action != "R") {
					return Snapshot{}, errors.New("invalid copy ancestry")
				}
				needed[c.CopyRevision] = true
				lastUse[c.CopyRevision] = rev.Number
			}
		}
	}
	tree := map[string]Object{"/": newObject(repo, 0, "", "/", "dir")}
	history := map[int64]map[string]Object{0: clone(tree)}
	retainedNodes := 1
	for _, rev := range revisions {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		before := clone(tree)
		deleted := func(p string) bool {
			for _, c := range rev.Changes {
				if (c.Action == "D" || c.Action == "R") && under(p, c.Path) {
					return true
				}
			}
			return false
		}
		for _, c := range rev.Changes {
			if c.Action == "D" || c.Action == "R" {
				for p := range tree {
					if under(p, c.Path) {
						delete(tree, p)
					}
				}
			}
		}
		changes := append([]Change(nil), rev.Changes...)
		sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
		for _, c := range changes {
			// A copied directory can contain explicit deletes/replacements in
			// this same commit. Apply these after the parent's copy as well.
			if c.Action == "D" || c.Action == "R" {
				for p := range tree {
					if under(p, c.Path) {
						delete(tree, p)
					}
				}
			}
			if c.Action != "A" && c.Action != "R" {
				continue
			}
			if c.CopyFrom == "" {
				if c.Kind != "file" && c.Kind != "dir" {
					return Snapshot{}, errors.New("unknown added node kind")
				}
				tree[c.Path] = newObject(repo, rev.Number, rev.Author, c.Path, c.Kind)
				continue
			}
			source, ok := history[c.CopyRevision]
			if !ok {
				return Snapshot{}, errors.New("copy revision unavailable")
			}
			if _, ok := source[c.CopyFrom]; !ok {
				return Snapshot{}, errors.New("copy source unavailable")
			}
			for p, old := range source {
				if err := ctx.Err(); err != nil {
					return Snapshot{}, err
				}
				if !under(p, c.CopyFrom) {
					continue
				}
				destination := c.Path + strings.TrimPrefix(p, c.CopyFrom)
				if c.CopyFrom == "/" {
					destination = path.Join(c.Path, p)
				}
				continuation := deleted(p) && before[p].ID == old.ID
				if continuation {
					uses := 0
					for _, other := range changes {
						if other.CopyFrom != "" && under(p, other.CopyFrom) && history[other.CopyRevision][p].ID == old.ID {
							uses++
						}
					}
					if uses != 1 {
						return Snapshot{}, errors.New("ambiguous object continuation")
					}
					tree[destination] = old
				} else {
					tree[destination] = newObject(repo, rev.Number, rev.Author, destination, old.Kind)
				}
			}
		}
		if needed[rev.Number] {
			// Bound the materialized ancestry cache independently of XML size.
			// Over-budget histories are unavailable, never guessed ownership.
			if retainedNodes+len(tree) > 500000 {
				return Snapshot{}, errors.New("ownership ancestry cache exceeds node budget")
			}
			history[rev.Number] = clone(tree)
			retainedNodes += len(tree)
		}
		for _, c := range changes {
			if c.CopyFrom != "" && lastUse[c.CopyRevision] == rev.Number {
				retainedNodes -= len(history[c.CopyRevision])
				delete(history, c.CopyRevision)
			}
		}
	}
	result := Snapshot{Revision: head, Entries: []Entry{}}
	for p, o := range tree {
		if o.Kind == "file" {
			result.Entries = append(result.Entries, Entry{Path: strings.TrimPrefix(p, "/"), Object: o})
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	return result, nil
}
