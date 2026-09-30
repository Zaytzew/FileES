// Package releasenotes is the signed "what's new" list of one release.
//
// A notes.json sits next to the manifests in the immutable release directory
// and is signed with the release key like them. It is written for people
// deciding whether to download: new features and the fixes a user notices,
// never a changelog of internal work (owner, 2026-09-29). Every item is in
// Polish and in English.
//
// Every file also carries the items of earlier releases of the same kind,
// each marked with the sequence of the release that introduced it. A page
// that skips releases — the beta page after several alphas, or a server
// promoted from alpha to beta — shows everything newer than what it
// published before by reading only the current release. Pruning old release
// directories from FILEES-BIN therefore never loses what they introduced.
package releasenotes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Schema names this format.
const Schema = "filees.release-notes/v1"

// FileName is the notes document inside releases/<id>/; the signature is
// FileName + ".sig" and DraftName is what the draft tool writes for review.
const (
	FileName  = "notes.json"
	DraftName = "notes.draft.json"
)

const (
	// MaxNewItems bounds what one release may introduce.
	MaxNewItems = 20
	// MaxItems bounds the whole file, carried history included.
	MaxItems = 60
	// MaxTextRunes bounds one sentence. A card shows a hint, not an essay.
	MaxTextRunes = 160
	// MaxFileSize bounds what a reader accepts before parsing.
	MaxFileSize = 64 << 10
)

// Kinds an item may have. Feature is the default.
const (
	KindFeature  = "feature"
	KindFix      = "fix"
	KindAdmin    = "admin"
	KindSecurity = "security"
)

// Notes is one release's document.
type Notes struct {
	Schema    string `json:"schema"`
	ReleaseID string `json:"release_id"`
	Sequence  uint64 `json:"sequence"`
	// Component is server, desktop or android: the kind of release, which
	// decides the scopes its items may have.
	Component string `json:"component"`
	// SVNRevision is the source revision the release was built from; the
	// next draft of this component starts after it.
	SVNRevision string `json:"svn_revision,omitempty"`
	// HistoryFrom is the lowest sequence from which every release's items
	// are still carried. A reader that published an older release than that
	// is told the list is incomplete.
	HistoryFrom uint64 `json:"history_from"`
	Items       []Item `json:"items"`
}

// Item is one sentence on one card.
type Item struct {
	// Sequence is the release that introduced the item.
	Sequence uint64 `json:"sequence"`
	// Scope is the card: server, desktop (Windows and Linux), windows,
	// linux or android.
	Scope string `json:"scope"`
	Kind  string `json:"kind,omitempty"`
	PL    string `json:"pl"`
	EN    string `json:"en"`
}

var componentScopes = map[string][]string{
	"server":  {"server"},
	"desktop": {"desktop", "windows", "linux"},
	"android": {"android"},
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ScopeAllowed reports whether a release of component may carry scope.
func ScopeAllowed(component, scope string) bool {
	for _, allowed := range componentScopes[component] {
		if allowed == scope {
			return true
		}
	}
	return false
}

// Parse decodes and validates a reviewed, complete document.
func Parse(data []byte) (*Notes, error) {
	return parse(data, true)
}

// ParseDraft accepts what Parse accepts plus items still missing one
// language, which is the state the draft tool leaves them in.
func ParseDraft(data []byte) (*Notes, error) {
	return parse(data, false)
}

func parse(data []byte, complete bool) (*Notes, error) {
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("release notes exceed %d bytes", MaxFileSize)
	}
	var notes Notes
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&notes); err != nil {
		return nil, fmt.Errorf("release notes: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("release notes: trailing data after the document")
	}
	if err := notes.validate(complete); err != nil {
		return nil, err
	}
	return &notes, nil
}

// Validate checks a complete document.
func (n *Notes) Validate() error { return n.validate(true) }

func (n *Notes) validate(complete bool) error {
	if n.Schema != Schema {
		return fmt.Errorf("release notes schema %q, want %q", n.Schema, Schema)
	}
	if !identifierPattern.MatchString(n.ReleaseID) {
		return errors.New("release notes release_id is not a plain identifier")
	}
	if n.Sequence == 0 {
		return errors.New("release notes sequence must be positive")
	}
	if _, known := componentScopes[n.Component]; !known {
		return fmt.Errorf("release notes component %q is not server, desktop or android", n.Component)
	}
	if n.SVNRevision != "" && strings.Trim(n.SVNRevision, "0123456789") != "" {
		return errors.New("release notes svn_revision must be a number")
	}
	if n.HistoryFrom == 0 || n.HistoryFrom > n.Sequence {
		return fmt.Errorf("release notes history_from %d must be between 1 and the sequence %d", n.HistoryFrom, n.Sequence)
	}
	if len(n.Items) > MaxItems {
		return fmt.Errorf("release notes carry %d items, at most %d", len(n.Items), MaxItems)
	}
	current := 0
	seen := map[string]bool{}
	for i, item := range n.Items {
		where := fmt.Sprintf("release notes item %d", i+1)
		if item.Sequence < n.HistoryFrom || item.Sequence > n.Sequence {
			return fmt.Errorf("%s: sequence %d is outside %d..%d", where, item.Sequence, n.HistoryFrom, n.Sequence)
		}
		if item.Sequence == n.Sequence {
			current++
		}
		if !ScopeAllowed(n.Component, item.Scope) {
			return fmt.Errorf("%s: scope %q does not belong to a %s release", where, item.Scope, n.Component)
		}
		switch item.Kind {
		case "", KindFeature, KindFix, KindSecurity:
		case KindAdmin:
			if n.Component != "server" {
				return fmt.Errorf("%s: kind admin is for server releases", where)
			}
		default:
			return fmt.Errorf("%s: kind %q is not feature, fix, admin or security", where, item.Kind)
		}
		for _, text := range []struct{ lang, value string }{{"pl", item.PL}, {"en", item.EN}} {
			if text.value == "" {
				if complete {
					return fmt.Errorf("%s: %s is empty; every item is in Polish and English", where, text.lang)
				}
				continue
			}
			if err := checkText(text.value); err != nil {
				return fmt.Errorf("%s: %s %w", where, text.lang, err)
			}
		}
		// One release may not say the same thing twice; two releases may
		// (a general "Security fixes" line recurs, and each release carries
		// the previous ones' items).
		key := fmt.Sprint(item.Sequence) + "\x00" + item.Scope + "\x00" + strings.ToLower(item.PL) + "\x00" + strings.ToLower(item.EN)
		if seen[key] {
			return fmt.Errorf("%s repeats an earlier item of the same release", where)
		}
		seen[key] = true
	}
	if current > MaxNewItems {
		return fmt.Errorf("release %s introduces %d items, at most %d", n.ReleaseID, current, MaxNewItems)
	}
	return nil
}

// checkText keeps an item a plain sentence: nothing a page could render as
// something else, and nothing that reorders the text around it.
func checkText(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("is not UTF-8")
	}
	if strings.TrimSpace(text) != text {
		return errors.New("has leading or trailing space")
	}
	if n := utf8.RuneCountInString(text); n > MaxTextRunes {
		return fmt.Errorf("has %d characters, at most %d", n, MaxTextRunes)
	}
	for _, r := range text {
		// Cc covers line breaks and tabs, Cf the bidi and zero-width marks.
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return fmt.Errorf("contains control or formatting character U+%04X", r)
		}
	}
	return nil
}

// CheckSecurityEpoch refuses a release that raises the security epoch
// without saying so. Such a release blocks every way back to the version
// before it, so its card must tell users the update matters: at least one
// security item of its own, in both languages. A general sentence ("Security
// fixes") is enough; the details of the vulnerability do not belong on a
// public page (owner, 2026-09-29). Zero for either epoch means unknown and
// checks nothing.
func (n *Notes) CheckSecurityEpoch(previous, current uint64) error {
	if previous == 0 || current <= previous {
		return nil
	}
	for _, item := range n.Items {
		if item.Sequence == n.Sequence && item.Kind == KindSecurity && item.PL != "" && item.EN != "" {
			return nil
		}
	}
	return fmt.Errorf("release %s raises security_epoch from %d to %d but introduces no security item; add one in both languages, a general sentence is enough", n.ReleaseID, previous, current)
}

// AddSecurityPlaceholder gives a draft of an epoch-raising release an empty
// security item for the reviewer to fill in, unless it already has its own.
// It reports whether it added one.
func (n *Notes) AddSecurityPlaceholder(previous, current uint64) bool {
	if previous == 0 || current <= previous {
		return false
	}
	for _, item := range n.Items {
		if item.Sequence == n.Sequence && item.Kind == KindSecurity {
			return false
		}
	}
	n.Items = append(n.Items, Item{Sequence: n.Sequence, Scope: componentScopes[n.Component][0], Kind: KindSecurity})
	return true
}

// Selection is what one card shows.
type Selection struct {
	Items []Item
	// Incomplete is set when the previously published release is older than
	// the history this document still carries.
	Incomplete bool
}

// Since returns the items of the given scopes introduced after the release
// with sequence after, most important first. after == 0 means nothing was
// published before on this page: only the current release's own items are
// shown, not the whole carried history.
func (n *Notes) Since(after uint64, scopes ...string) Selection {
	var selection Selection
	wanted := map[string]bool{}
	for _, scope := range scopes {
		wanted[scope] = true
	}
	for _, item := range n.Items {
		if !wanted[item.Scope] {
			continue
		}
		if after == 0 {
			if item.Sequence != n.Sequence {
				continue
			}
		} else if item.Sequence <= after {
			continue
		}
		selection.Items = append(selection.Items, item)
	}
	if after != 0 && after+1 < n.HistoryFrom {
		selection.Incomplete = true
	}
	sort.SliceStable(selection.Items, func(i, j int) bool {
		a, b := selection.Items[i], selection.Items[j]
		if rank(a.Kind) != rank(b.Kind) {
			return rank(a.Kind) < rank(b.Kind)
		}
		return a.Sequence > b.Sequence
	})
	// A page that skipped releases would otherwise list a recurring
	// sentence once per release; the newest one stays.
	seen := map[string]bool{}
	unique := selection.Items[:0]
	for _, item := range selection.Items {
		key := item.Scope + "\x00" + item.Kind + "\x00" + strings.ToLower(item.PL) + "\x00" + strings.ToLower(item.EN)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, item)
	}
	selection.Items = unique
	return selection
}

func rank(kind string) int {
	switch kind {
	case KindSecurity:
		return 0
	case KindAdmin:
		return 1
	case KindFix:
		return 3
	default:
		return 2
	}
}
