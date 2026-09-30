package releasenotes

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// LogEntry is one commit of `svn log --xml`.
type LogEntry struct {
	Revision uint64
	Message  string
}

// ParseSVNLog reads the output of `svn log --xml`.
func ParseSVNLog(r io.Reader) ([]LogEntry, error) {
	var log struct {
		Entries []struct {
			Revision uint64 `xml:"revision,attr"`
			Message  string `xml:"msg"`
		} `xml:"logentry"`
	}
	if err := xml.NewDecoder(io.LimitReader(r, 64<<20)).Decode(&log); err != nil {
		return nil, fmt.Errorf("svn log: %w", err)
	}
	entries := make([]LogEntry, 0, len(log.Entries))
	for _, entry := range log.Entries {
		entries = append(entries, LogEntry{Revision: entry.Revision, Message: entry.Message})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Revision < entries[j].Revision })
	return entries, nil
}

// A commit message line announcing an item, for example
//
//	Co nowego [desktop]: Pobieranie dużych plików strumieniem
//	What's new [desktop]: Large files download as a stream
//	Co nowego [server/admin]: Import dumpa sprawdza wolne miejsce
//
// The n-th Polish line of one scope/kind pairs with the n-th English line of
// the same scope/kind in the same commit.
var announcement = regexp.MustCompile(`^\s*(Co nowego|What['’]s new)\s*\[\s*([a-z]+)\s*(?:/\s*([a-z]+)\s*)?\]\s*:\s*(.*?)\s*$`)

// Draft builds the next notes document of a component: the previous
// release's items carried forward, plus one item per announcement in the
// commits since. Items missing a language keep it empty for the reviewer;
// ParseDraft accepts them, Parse does not.
func Draft(previous *Notes, entries []LogEntry, component, releaseID string, sequence uint64, svnRevision string) (*Notes, []string, error) {
	if _, known := componentScopes[component]; !known {
		return nil, nil, fmt.Errorf("component %q is not server, desktop or android", component)
	}
	var warnings []string
	notes := &Notes{Schema: Schema, ReleaseID: releaseID, Sequence: sequence, Component: component, SVNRevision: svnRevision, HistoryFrom: sequence}
	if previous != nil {
		if previous.Component != component {
			return nil, nil, fmt.Errorf("previous notes are for %s, not %s", previous.Component, component)
		}
		if previous.Sequence >= sequence {
			return nil, nil, fmt.Errorf("previous notes have sequence %d, not older than %d", previous.Sequence, sequence)
		}
		notes.HistoryFrom = previous.HistoryFrom
		notes.Items = append(notes.Items, previous.Items...)
	}
	var fresh []Item
	for _, entry := range entries {
		type key struct{ scope, kind string }
		pl := map[key][]string{}
		en := map[key][]string{}
		var order []key
		for _, line := range strings.Split(strings.ReplaceAll(entry.Message, "\r\n", "\n"), "\n") {
			match := announcement.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			k := key{match[2], match[3]}
			if !ScopeAllowed(component, k.scope) {
				if !knownScope(k.scope) {
					warnings = append(warnings, fmt.Sprintf("r%d: unknown scope %q ignored", entry.Revision, k.scope))
				}
				continue
			}
			if _, ok := pl[k]; !ok {
				if _, ok := en[k]; !ok {
					order = append(order, k)
				}
			}
			if strings.HasPrefix(match[1], "Co") {
				pl[k] = append(pl[k], match[4])
			} else {
				en[k] = append(en[k], match[4])
			}
		}
		for _, k := range order {
			count := max(len(pl[k]), len(en[k]))
			for i := 0; i < count; i++ {
				item := Item{Sequence: sequence, Scope: k.scope, Kind: k.kind}
				if item.Kind == KindFeature {
					item.Kind = ""
				}
				if i < len(pl[k]) {
					item.PL = pl[k][i]
				}
				if i < len(en[k]) {
					item.EN = en[k][i]
				}
				if item.PL == "" || item.EN == "" {
					warnings = append(warnings, fmt.Sprintf("r%d: [%s] has no counterpart in the other language; fill it in before review", entry.Revision, k.scope))
				}
				if repeated(fresh, item) {
					warnings = append(warnings, fmt.Sprintf("r%d: [%s] repeats an announcement of an earlier commit; listed once", entry.Revision, k.scope))
					continue
				}
				fresh = append(fresh, item)
			}
		}
	}
	notes.Items = append(notes.Items, fresh...)
	// Carried history is the first thing to go when the file gets too long:
	// whole releases, oldest first, so what remains is complete from
	// HistoryFrom on.
	for len(notes.Items) > MaxItems {
		oldest := notes.Items[0].Sequence
		for _, item := range notes.Items {
			oldest = min(oldest, item.Sequence)
		}
		if oldest == sequence {
			break
		}
		kept := notes.Items[:0]
		for _, item := range notes.Items {
			if item.Sequence != oldest {
				kept = append(kept, item)
			}
		}
		notes.Items = kept
		notes.HistoryFrom = oldest + 1
	}
	if err := notes.validate(false); err != nil {
		return nil, warnings, err
	}
	if len(fresh) == 0 {
		warnings = append(warnings, "no announcements in the commits since the previous release; the card will show no new items")
	}
	return notes, warnings, nil
}

// repeated reports whether an identical announcement is already in this
// release: several commits may carry the same general line, e.g. "Poprawki
// bezpieczeństwa" (live r1757-beta: the draft stopped on it instead).
func repeated(items []Item, item Item) bool {
	for _, have := range items {
		if have.Scope == item.Scope && have.Kind == item.Kind &&
			strings.EqualFold(have.PL, item.PL) && strings.EqualFold(have.EN, item.EN) {
			return true
		}
	}
	return false
}

func knownScope(scope string) bool {
	for _, scopes := range componentScopes {
		for _, s := range scopes {
			if s == scope {
				return true
			}
		}
	}
	return false
}
