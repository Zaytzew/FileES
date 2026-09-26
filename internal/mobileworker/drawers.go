package mobileworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"filees/pkg/guiblob"
	v1 "filees/pkg/mobile/v1"
)

// ErrDrawersUnavailable means this server has no drawer storage wired in
// (Browser.Drawers is nil) - a capability gap, never sent for a realm that
// simply has no drawers. An absent or empty document is not an error: the
// owner's call is that drawers are a GUI option, and a user who never
// touched them, or who wants a flat list, is the normal case.
var ErrDrawersUnavailable = errors.New("mobile: drawer projection is unavailable")

// DrawerReader reads the same realm-scoped GUI blob storage the desktop's
// GET_GUI_BLOB/SET_GUI_BLOB already uses (implementation notes (not distributed)
// §5), through a read-only path that never carries a Write. Implementations
// must not accept one - this interface has no method that could.
type DrawerReader interface {
	Read(ctx context.Context, realmID string) (guiblob.State, error)
}

const drawerSchema = "filees.gui.drawers/v1"

// drawerDocument mirrors cmd/filees-gui-wails/frontend/drawer-layout.js's
// filees.gui.drawers/v1 exactly. It is GUI-owned semantics private to this
// file - never exported, never the phone's wire type. ListDrawers below
// re-expresses it as v1.ListDrawersResult, the phone's own contract.
type drawerDocument struct {
	Schema  string            `json:"schema"`
	Drawers []drawerEntry     `json:"drawers"`
	Repos   map[string]string `json:"repos"`
}

type drawerEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// parseDrawerDocument mirrors drawer-layout.js's parseDrawers, including its
// limits, so a document the GUI itself would refuse to load never reaches
// the phone as if it were fine. Empty data is the empty document, not an
// error - most realms never create a drawer.
func parseDrawerDocument(data string) (drawerDocument, error) {
	if data == "" {
		return drawerDocument{Repos: map[string]string{}}, nil
	}
	var doc drawerDocument
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return drawerDocument{}, errors.New("drawer document is malformed")
	}
	if doc.Schema != drawerSchema || doc.Repos == nil || len(doc.Drawers) > 128 {
		return drawerDocument{}, errors.New("drawer document is invalid")
	}
	ids := make(map[string]struct{}, len(doc.Drawers))
	for _, d := range doc.Drawers {
		name := strings.TrimSpace(d.Name)
		if !drawerIDMatches(d.ID) || name == "" || utf8.RuneCountInString(name) > 80 {
			return drawerDocument{}, errors.New("drawer document is invalid")
		}
		if _, dup := ids[d.ID]; dup {
			return drawerDocument{}, errors.New("drawer document is invalid")
		}
		ids[d.ID] = struct{}{}
	}
	if len(doc.Repos) > 2048 {
		return drawerDocument{}, errors.New("drawer document is invalid")
	}
	for repo, id := range doc.Repos {
		if !drawerIDMatches(repo) {
			return drawerDocument{}, errors.New("drawer document is invalid")
		}
		if _, ok := ids[id]; !ok {
			return drawerDocument{}, errors.New("drawer document is invalid")
		}
	}
	return doc, nil
}

func drawerIDMatches(id string) bool {
	if len(id) < 1 || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ListDrawers is a read-only viewer of the desktop's own drawer
// organization. It never returns a domain error for "no drawers" - that is
// the ordinary state of a realm that never opted into them.
func (b Browser) ListDrawers(ctx context.Context, clientID string) (v1.ListDrawersResult, error) {
	if b.Drawers == nil {
		return v1.ListDrawersResult{}, ErrDrawersUnavailable
	}
	proj, err := b.Authority.List(ctx, clientID)
	if err != nil {
		return v1.ListDrawersResult{}, err
	}
	state, err := b.Drawers.Read(ctx, proj.RealmID)
	if err != nil {
		return v1.ListDrawersResult{}, err
	}
	doc, err := parseDrawerDocument(state.Data)
	if err != nil {
		return v1.ListDrawersResult{}, err
	}
	drawers := make([]v1.DrawerSummary, 0, len(doc.Drawers))
	for _, d := range doc.Drawers {
		drawers = append(drawers, v1.DrawerSummary{ID: d.ID, Name: strings.TrimSpace(d.Name)})
	}
	assignments := make(map[string]string, len(doc.Repos))
	for repo, id := range doc.Repos {
		assignments[repo] = id
	}
	res := v1.ListDrawersResult{Version: state.Version, Drawers: drawers, Assignments: assignments}
	if err := res.Validate(); err != nil {
		return v1.ListDrawersResult{}, fmt.Errorf("drawer projection: %w", err)
	}
	return res, nil
}
